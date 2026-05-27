package websocket

import (
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/xs23933/core/v3"
)

type WS struct {
	onConnect func(*Conn)
	onMessage func(*Conn, MessageType, []byte)
	onClose   func(*Conn)
}

func New() *WS {
	return &WS{}
}

func (w *WS) OnConnect(f func(*Conn)) *WS {
	w.onConnect = f
	return w
}

func (w *WS) OnMessage(f func(*Conn, MessageType, []byte)) *WS {
	w.onMessage = f
	return w
}

func (w *WS) OnClose(f func(*Conn)) *WS {
	w.onClose = f
	return w
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (w *WS) Handler() func(core.Ctx) error {
	return func(c core.Ctx) error {

		wsConn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
		if err != nil {
			core.Erro("upgrade websocket failed: %v", err)
			return err
		}

		conn := newConn(wsConn)

		DefaultManager.Add(conn)

		if w.onConnect != nil {
			w.onConnect(conn)
		}

		go conn.writeLoop()
		conn.readLoop(w.onMessage)

		if w.onClose != nil {
			w.onClose(conn)
		}

		return nil
	}
}
