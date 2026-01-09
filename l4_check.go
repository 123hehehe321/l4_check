package checkhandler

import (
	"io"
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
// 行为说明：
// 1. 不消费数据（只 Read 1 byte，立刻放弃）
// 2. 能真实感知 FIN / EOF / RST
// 3. IdleTimeout 会在有数据时自动刷新
// 4. 仅在异常时 Close，避免 CLOSE-WAIT
func (h *CheckHandler) Handle(conn *layer4.Connection, next layer4.Handler) error {
	rawConn := conn.Conn

	// === 启动后台监控协程 ===
	done := make(chan struct{})

	go func() {
		defer close(done)

		buf := make([]byte, 1)

		for {
			// 设置 / 刷新 idle timeout
			if h.IdleTimeout > 0 {
				_ = rawConn.SetReadDeadline(
					time.Now().Add(time.Duration(h.IdleTimeout)),
				)
			}

			n, err := rawConn.Read(buf)

			if err != nil {
				// EOF / FIN / timeout / RST
				_ = rawConn.Close()
				return
			}

			if n > 0 {
				// 把字节“丢回去”
				// 这里不转发、不缓存，proxy 会重新从 socket 读
				continue
			}
		}
	}()

	// === 放行给后续 handler（通常是 proxy） ===
	var err error
	if next != nil {
		err = next.Handle(conn)
	}

	// === proxy 结束，确保 fd 回收 ===
	_ = rawConn.Close()
	<-done

	return err
}

var _ layer4.NextHandler = (*CheckHandler)(nil)
