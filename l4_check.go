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
	// IdleTimeout 指连接在无任何读活动下允许存活的最大时间
	IdleTimeout caddy.Duration `json:"idle_timeout,omitempty"`
}

func (CheckHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "layer4.handlers.check",
		New: func() caddy.Module { return new(CheckHandler) },
	}
}

// Handle 实现 layer4.NextHandler
//
// 设计说明：
// - 不读取 socket（绝不抢 proxy 的数据）
// - 仅设置 ReadDeadline
// - timeout 由 proxy 的 Read 触发
// - proxy 返回后，确保 fd 被关闭，避免 CLOSE-WAIT
func (h *CheckHandler) Handle(conn *layer4.Connection, next layer4.Handler) error {
	rawConn := conn.Conn

	// 设置 idle timeout（关键）
	if h.IdleTimeout > 0 {
		_ = rawConn.SetReadDeadline(
			time.Now().Add(time.Duration(h.IdleTimeout)),
		)
	}

	var err error
	if next != nil {
		err = next.Handle(conn)
	}

	// proxy 结束后，兜底关闭 fd
	_ = rawConn.Close()

	return err
}

var _ layer4.NextHandler = (*CheckHandler)(nil)
