package protocol

import (
	"context"
	"io"
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

// write is the one place a message reaches the socket. Encoding happens before
// the lock is taken, so nothing but the write itself is serialized.
func (c *Conn) write(ctx context.Context, msg []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.Write(ctx, websocket.MessageBinary, msg)
}

// Send writes one message.
func (c *Conn) Send(ctx context.Context, kind Kind, payload []byte) error {
	return c.write(ctx, Encode(kind, payload))
}

// SendJSON writes one control message.
func (c *Conn) SendJSON(ctx context.Context, kind Kind, v any) error {
	msg, err := EncodeJSON(kind, v)
	if err != nil {
		return err
	}
	return c.write(ctx, msg)
}

// Pump forwards r to the peer as kind messages until r ends or the peer stops
// listening, and reports how many bytes it moved.
//
// The read buffer reserves its first byte for the kind tag, so a chunk travels
// from the pipe to the socket without being copied into a second buffer on the
// way. The websocket library masks into a buffer of its own, so it never
// writes back into this one.
func (c *Conn) Pump(ctx context.Context, kind Kind, r io.Reader) int64 {
	frame := make([]byte, ChunkSize+1)
	frame[0] = byte(kind)
	var total int64
	for {
		n, err := r.Read(frame[1:])
		if n > 0 {
			total += int64(n)
			if err := c.write(ctx, frame[:n+1]); err != nil {
				return total
			}
		}
		if err != nil {
			return total
		}
	}
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
