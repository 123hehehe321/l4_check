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
// 职责说明：
// 1. 在连接建立后尽早设置 TCP keepalive
// 2. 不干预数据流、不设置 deadline
// 3. 不参与连接关闭决策
// 4. 仅在配置错误（没有下游 handler）时兜底关闭
//
// 这是一个“纯初始化型”的 Layer4 handler
type CheckHandler struct {
	// TCP keepalive 探测间隔
	// 仅作用于 Client -> Caddy 这一段
	TCPKeepAlivePeriod caddy.Duration `json:"tcp_keepalive_period,omitempty"`
}

func (CheckHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "layer4.handlers.check",
		New: func() caddy.Module { return new(CheckHandler) },
	}
}

func (h *CheckHandler) Handle(conn *layer4.Connection, next layer4.Handler) error {
	raw := conn.Conn

	// === TCP keepalive 初始化 ===
	if tc, ok := raw.(*net.TCPConn); ok {
		// 开启 keepalive
		_ = tc.SetKeepAlive(true)

		// 设置 keepalive 周期（如果配置了）
		if h.TCPKeepAlivePeriod > 0 {
			_ = tc.SetKeepAlivePeriod(time.Duration(h.TCPKeepAlivePeriod))
		}
	}

	// === 配置错误保护 ===
	// check handler 之后必须有下游（通常是 proxy）
	if next == nil {
		_ = raw.Close()
		return nil
	}

	// === 完全交由下游 handler（proxy）处理 ===
	// 不捕获、不包装、不额外 close
	return next.Handle(conn)
}

// 编译期接口断言
var _ layer4.NextHandler = (*CheckHandler)(nil)
