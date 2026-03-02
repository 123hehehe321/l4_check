package checkhandler

import (
    "fmt"
    "net"
    "sync"
    "sync/atomic"
    "syscall"
    "time"

    "github.com/caddyserver/caddy/v2"
    "github.com/mholt/caddy-l4/layer4"
)

func init() {
    caddy.RegisterModule(Handler{})
}

type Handler struct {
    TCPKeepAlive caddy.Duration `json:"tcp_keepalive,omitempty"`
    IdleTimeout  caddy.Duration `json:"idle_timeout,omitempty"`
    MaxLifetime  caddy.Duration `json:"max_lifetime,omitempty"`
    GracePeriod  caddy.Duration `json:"grace_period,omitempty"`
    ForceReset   bool           `json:"force_reset,omitempty"`
}

func (Handler) CaddyModule() caddy.ModuleInfo {
    return caddy.ModuleInfo{
        ID:  "layer4.handlers.check",
        New: func() caddy.Module { return new(Handler) },
    }
}

func (h *Handler) Provision(_ caddy.Context) error {
    if h.GracePeriod <= 0 && h.MaxLifetime > 0 {
        h.GracePeriod = caddy.Duration(30 * time.Second)
    }
    return nil
}

func (h *Handler) Validate() error {
    idle      := time.Duration(h.IdleTimeout)
    maxLife   := time.Duration(h.MaxLifetime)
    grace     := time.Duration(h.GracePeriod)
    keepalive := time.Duration(h.TCPKeepAlive)

    if idle < 0 {
        return fmt.Errorf("check: idle_timeout must be >= 0, got %s", idle)
    }
    if maxLife < 0 {
        return fmt.Errorf("check: max_lifetime must be >= 0, got %s", maxLife)
    }
    if grace < 0 {
        return fmt.Errorf("check: grace_period must be >= 0, got %s", grace)
    }
    if keepalive < 0 {
        return fmt.Errorf("check: tcp_keepalive must be >= 0, got %s", keepalive)
    }
    if maxLife > 0 && idle == 0 {
        return fmt.Errorf("check: idle_timeout is required when max_lifetime is set")
    }
    if maxLife > 0 && idle > 0 && maxLife < idle {
        return fmt.Errorf(
            "check: max_lifetime (%s) must be >= idle_timeout (%s)", maxLife, idle,
        )
    }
    if idle > 0 && idle < 5*time.Second {
        return fmt.Errorf("check: idle_timeout (%s) too small, minimum 5s", idle)
    }
    if keepalive > 0 && keepalive < 5*time.Second {
        return fmt.Errorf("check: tcp_keepalive (%s) too small, minimum 5s", keepalive)
    }
    if grace > 0 && grace < 5*time.Second {
        return fmt.Errorf("check: grace_period (%s) too small, minimum 5s", grace)
    }
    return nil
}

func (h *Handler) Handle(cx *layer4.Connection, next layer4.Handler) error {
    raw := cx.Conn

    if tc, ok := raw.(*net.TCPConn); ok {
        _ = tc.SetKeepAlive(true)
        if h.TCPKeepAlive > 0 {
            _ = tc.SetKeepAlivePeriod(time.Duration(h.TCPKeepAlive))
        }
    }

    ic := newIdleConn(
        raw,
        time.Duration(h.IdleTimeout),
        time.Duration(h.GracePeriod),
        h.ForceReset,
    )

    if h.MaxLifetime > 0 {
        ic.setMaxLifetime(time.Duration(h.MaxLifetime))
    }

    cx.Conn = ic
    defer ic.Close() // 兜底关闭，once 保证不会双重关闭

    if next != nil {
        return next.Handle(cx)
    }
    return nil
}

// ==================== idleConn ====================

const (
    stateNormal int32 = 0
    stateGrace  int32 = 1
    stateClosed int32 = 2
)

type idleConn struct {
    net.Conn
    idleTimeout time.Duration
    gracePeriod time.Duration
    forceReset  bool

    state atomic.Int32
    once  sync.Once
    err   error

    mu            sync.Mutex
    idleTimer     *time.Timer
    graceTimer    *time.Timer
    lifetimeTimer *time.Timer
}

func newIdleConn(raw net.Conn, idleTimeout, gracePeriod time.Duration, forceReset bool) *idleConn {
    ic := &idleConn{
        Conn:        raw,
        idleTimeout: idleTimeout,
        gracePeriod: gracePeriod,
        forceReset:  forceReset,
    }
    if idleTimeout > 0 {
        ic.idleTimer = time.AfterFunc(idleTimeout, func() {
            ic.shutdown()
        })
    }
    return ic
}

func (c *idleConn) setMaxLifetime(d time.Duration) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.lifetimeTimer = time.AfterFunc(d, func() {
        c.enterGrace()
    })
}

func (c *idleConn) Read(b []byte) (int, error) {
    n, err := c.Conn.Read(b)
    if n > 0 {
        c.touch()
    }
    return n, err
}

func (c *idleConn) Write(b []byte) (int, error) {
    n, err := c.Conn.Write(b)
    if n > 0 {
        c.touch()
    }
    return n, err
}

func (c *idleConn) touch() {
    if c.state.Load() == stateClosed {
        return
    }
    c.mu.Lock()
    defer c.mu.Unlock()

    switch c.state.Load() {
    case stateNormal:
        if c.idleTimer != nil {
            c.idleTimer.Reset(c.idleTimeout)
        }
    case stateGrace:
        if c.graceTimer != nil {
            c.graceTimer.Reset(c.gracePeriod)
        }
    }
}

func (c *idleConn) enterGrace() {
    if !c.state.CompareAndSwap(stateNormal, stateGrace) {
        return
    }

    c.mu.Lock()
    if c.idleTimer != nil {
        c.idleTimer.Stop()
    }
    needShutdown := c.gracePeriod <= 0
    if !needShutdown {
        c.graceTimer = time.AfterFunc(c.gracePeriod, func() {
            c.shutdown()
        })
    }
    c.mu.Unlock() // 先解锁，避免 shutdown() 内死锁

    if needShutdown {
        c.shutdown()
    }
}

func (c *idleConn) shutdown() {
    for {
        s := c.state.Load()
        if s == stateClosed {
            return
        }
        if c.state.CompareAndSwap(s, stateClosed) {
            break
        }
    }
    c.stopAllTimers()
    c.once.Do(func() {
        c.err = c.doClose()
    })
}

func (c *idleConn) Close() error {
    c.shutdown()
    return c.err
}

func (c *idleConn) doClose() error {
    if c.forceReset {
        return resetClose(c.Conn)
    }
    return c.Conn.Close()
}

func resetClose(conn net.Conn) error {
    if tc, ok := conn.(*net.TCPConn); ok {
        _ = tc.SetLinger(0)
        return tc.Close()
    }
    if sc, ok := conn.(syscall.Conn); ok {
        raw, err := sc.SyscallConn()
        if err == nil {
            _ = raw.Control(func(fd uintptr) {
                _ = syscall.SetsockoptLinger(
                    int(fd),
                    syscall.SOL_SOCKET,
                    syscall.SO_LINGER,
                    &syscall.Linger{Onoff: 1, Linger: 0},
                )
            })
        }
    }
    return conn.Close()
}

func (c *idleConn) stopAllTimers() {
    c.mu.Lock()
    defer c.mu.Unlock()
    if c.idleTimer != nil {
        c.idleTimer.Stop()
    }
    if c.graceTimer != nil {
        c.graceTimer.Stop()
    }
    if c.lifetimeTimer != nil {
        c.lifetimeTimer.Stop()
    }
}

var _ layer4.NextHandler = (*Handler)(nil)
