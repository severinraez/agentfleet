package protocol

import (
	"context"
	"sync"

	"github.com/coder/websocket"
)

// Conn is a websocket carrying protocol messages.
//
// The hub writes from three places at once — the stdout pump, the stderr pump
// and whoever reports the exit — so writes are serialized here rather than
// left to chance.
type Conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

// NewConn wraps a websocket connection.
func NewConn(ws *websocket.Conn) *Conn {
	ws.SetReadLimit(ReadLimit)
	return &Conn{ws: ws}
}

// Send writes one message.
func (c *Conn) Send(ctx context.Context, kind Kind, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.Write(ctx, websocket.MessageBinary, Encode(kind, payload))
}

// SendJSON writes one control message.
func (c *Conn) SendJSON(ctx context.Context, kind Kind, v any) error {
	msg, err := EncodeJSON(kind, v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.Write(ctx, websocket.MessageBinary, msg)
}

// Recv reads the next message.
func (c *Conn) Recv(ctx context.Context) (Message, error) {
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return Message{}, err
	}
	return Decode(data)
}

// Ping sends a ping and waits for the pong, so that a peer which vanished
// without closing its side is noticed. It does not bound how long a call runs.
func (c *Conn) Ping(ctx context.Context) error { return c.ws.Ping(ctx) }

// Close ends the connection politely.
func (c *Conn) Close(reason string) error {
	return c.ws.Close(websocket.StatusNormalClosure, reason)
}

// CloseNow drops the connection without a handshake.
func (c *Conn) CloseNow() error { return c.ws.CloseNow() }
