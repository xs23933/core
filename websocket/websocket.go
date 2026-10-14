package websocket

import (
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/xs23933/core/v3"
)

type WS struct {
	onConnect func(*Conn)
	onMessage func(*Conn, MessageType, []byte)
	onClose   func(*Conn)
	options   Options
}

func New() *WS                                                 { return &WS{} }
func (w *WS) OnConnect(f func(*Conn)) *WS                      { w.onConnect = f; return w }
func (w *WS) OnMessage(f func(*Conn, MessageType, []byte)) *WS { w.onMessage = f; return w }
func (w *WS) OnClose(f func(*Conn)) *WS                        { w.onClose = f; return w }

// Handler 在当前请求内管理连接，不把 core.Ctx 保存到连接或后台协程。
func (w *WS) Handler() func(core.Ctx) error {
	options := w.options
	options.Subprotocols = append([]string(nil), options.Subprotocols...)
	origin := options.CheckOrigin
	if origin == nil {
		origin = func(*http.Request) bool { return true }
	}
	upgrader := websocket.Upgrader{Subprotocols: options.Subprotocols, CheckOrigin: origin, HandshakeTimeout: options.HandshakeTimeout}
	return func(c core.Ctx) error {
		if options.RequireSubprotocol {
			matched := false
			for _, offered := range websocket.Subprotocols(c.Request()) {
				for _, supported := range options.Subprotocols {
					if offered != "" && offered == supported {
						matched = true
						break
					}
				}
			}
			if !matched {
				return c.SendStatus(http.StatusBadRequest, "websocket subprotocol required")
			}
		}
		raw, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
		if err != nil {
			core.Erro("websocket upgrade failed error_type=%T", err)
			return err
		}
		conn := newConnWithOptions(raw, options, c.Request().Context())
		DefaultManager.Add(conn)
		var writer sync.WaitGroup
		defer func() {
			conn.Close()
			writer.Wait()
			if w.onClose != nil {
				w.onClose(conn)
			}
		}()
		if w.onConnect != nil {
			w.onConnect(conn)
		}
		writer.Go(func() { ; conn.writeLoop() })
		conn.readLoop(w.onMessage)
		return nil
	}
}
