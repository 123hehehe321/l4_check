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
// 生产级设计原则（修正版）：
// 1. 不读取 socket（绝不干扰 proxy 数据流）
// 2. 仅通过 ReadDeadline 控制连接生命周期
// 3. 正常连接生命周期完全交给 proxy
// 4. 仅在异常时兜底关闭，防止半死连接
// 5. 不制造 FIN-WAIT-2 / CLOSE-WAIT
// 6. 行为可预期、可长期运行
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

	// === TCP keepalive（安全、不会影响 proxy） ===
	if tc, ok := rawConn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)

		if h.TCPKeepAlivePeriod > 0 {
			_ = tc.SetKeepAlivePeriod(
				time.Duration(h.TCPKeepAlivePeriod),
			)
		}
	}

	// === Idle ReadDeadline（仅限制空闲，不主动 close） ===
	if h.IdleTimeout > 0 {
		_ = rawConn.SetReadDeadline(
			time.Now().Add(time.Duration(h.IdleTimeout)),
		)

		// handler 退出时清理，避免影响后续 handler
		defer rawConn.SetReadDeadline(time.Time{})
	}

	// === 交给下一个 handler（通常是 l4.proxy） ===
	if next != nil {
		err = next.Handle(conn)
	}

	// === 兜底策略：只在异常时关闭 ===
	//
	// 说明：
	// - err == nil：大多数是正常 EOF / 正常 FIN
	// - err != nil：copy 中断、deadline、对端异常
	if err != nil {
		_ = rawConn.Close()
	}

	return err
}

var _ layer4.NextHandler = (*CheckHandler)(nil)
