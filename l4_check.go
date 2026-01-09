package checkhandler

import (
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/mholt/caddy-l4/layer4"
)

func init() {
	caddy.RegisterModule(CheckHandler{})
}

type CheckHandler struct {
	// IdleTimeout 指连接在无数据活动下允许存活的最大时间
	IdleTimeout caddy.Duration `json:"idle_timeout,omitempty"`
}

func (CheckHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "layer4.handlers.check",
		New: func() caddy.Module { return new(CheckHandler) },
	}
}

// Handle 实现 layer4.NextHandler
// 作用：
// - 设置读超时，防止半开连接
// - 不主动读取数据，避免干扰 proxy
func (h *CheckHandler) Handle(conn *layer4.Connection, next layer4.Handler) error {
	rawConn := conn.Conn

	// 设置空闲超时（只设置，不读取）
	if h.IdleTimeout > 0 {
		_ = rawConn.SetReadDeadline(
			time.Now().Add(time.Duration(h.IdleTimeout)),
		)
	}

	if next != nil {
		err := next.Handle(conn)
		// 确保链路结束后 fd 被回收
		_ = rawConn.Close()
		return err
	}

	_ = rawConn.Close()
	return nil
}

var _ layer4.NextHandler = (*CheckHandler)(nil)
