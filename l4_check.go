package checkhandler

import (
	"io"
	"net"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/mholt/caddy-l4/layer4"
)

func init() {
	caddy.RegisterModule(CheckHandler{})
}

type CheckHandler struct {
	// IdleTimeout 指连接在无读事件下允许存活的最大时间
	IdleTimeout caddy.Duration `json:"idle_timeout,omitempty"`
}

func (CheckHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "layer4.handlers.check",
		New: func() caddy.Module { return new(CheckHandler) },
	}
}

// Handle 实现 layer4.NextHandler
// 功能：
// - 监控连接是否出现 EOF / FIN
// - 监控空闲超时
// - 一旦发现异常，立即 close，防止 CLOSE-WAIT
func (h *CheckHandler) Handle(conn *layer4.Connection, next layer4.Handler) error {
	rawConn := conn.Conn

	// 空闲超时保护
	if h.IdleTimeout > 0 {
		_ = rawConn.SetReadDeadline(time.Now().Add(time.Duration(h.IdleTimeout)))
	}

	// 启动 FIN / EOF 监控
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := rawConn.Read(buf)
			if err != nil {
				// EOF / timeout / RST
				_ = rawConn.Close()
				return
			}
			if n > 0 {
				// 刷新 idle timer
				if h.IdleTimeout > 0 {
					_ = rawConn.SetReadDeadline(
						time.Now().Add(time.Duration(h.IdleTimeout)),
					)
				}
			}
		}
	}()

	if next != nil {
		return next.Handle(conn)
	}

	// fallback
	_ = rawConn.Close()
	return nil
}

var _ layer4.NextHandler = (*CheckHandler)(nil)
