package checkhandler

import (
	"net"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/mholt/caddy-l4/layer4"
)

func init() {
	caddy.RegisterModule(CheckHandler{})
}

// CheckHandler
//
// 设计目标（生产版原则）：
// 1. 不读取 socket（绝不干扰 proxy 数据流）
// 2. 仅通过 ReadDeadline 控制连接生命周期
// 3. proxy 返回后，确保 fd 一定被关闭
// 4. 尽可能避免 CLOSE-WAIT / 半死连接
// 5. 行为可预期、可组合、可长期运行
type CheckHandler struct {
	// IdleTimeout 指连接在无任何读活动下允许存活的最大时间
	IdleTimeout caddy.Duration `json:"idle_timeout,omitempty"`

	// TCPKeepAlivePeriod 设置 TCP keepalive 探测周期
	// 为 0 表示使用系统默认
	TCPKeepAlivePeriod caddy.Duration `json:"tcp_keepalive_period,omitempty"`
}

func (CheckHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "layer4.handlers.check",
		New: func() caddy.Module { return new(CheckHandler) },
	}
}

// Handle 实现 layer4.NextHandler
func (h *CheckHandler) Handle(conn *layer4.Connection, next layer4.Handler) (err error) {
	rawConn := conn.Conn

	// === TCP 级别健壮性增强（不影响逻辑） ===
	if tc, ok := rawConn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)

		if h.TCPKeepAlivePeriod > 0 {
			_ = tc.SetKeepAlivePeriod(
				time.Duration(h.TCPKeepAlivePeriod),
			)
		}
	}

	// === Idle ReadDeadline 控制 ===
	if h.IdleTimeout > 0 {
		_ = rawConn.SetReadDeadline(
			time.Now().Add(time.Duration(h.IdleTimeout)),
		)

		// 确保 handler 退出后不污染后续逻辑
		defer rawConn.SetReadDeadline(time.Time{})
	}

	// === 最终兜底：无论发生什么，fd 必须关闭 ===
	defer rawConn.Close()

	// === 交给下一个 handler（通常是 l4.proxy） ===
	if next != nil {
		err = next.Handle(conn)
	}

	return err
}

var _ layer4.NextHandler = (*CheckHandler)(nil)
