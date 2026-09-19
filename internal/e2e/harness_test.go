package e2e

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/severinraez/agentfleet/internal/config"
	"github.com/severinraez/agentfleet/internal/hub"
	"github.com/severinraez/agentfleet/internal/protocol"
	"github.com/severinraez/agentfleet/internal/rpc"
)

// fleet is a hub on a loopback port with an rpc directory of its own.
type fleet struct {
	t     *testing.T
	dir   string
	work  string
	url   string
	audit *syncBuffer

	// stopHub cancels the hub's context; waitHub waits for it to have taken
	// every call in flight down with it.
	stopHub func()
	waitHub func() error
}

func start(t *testing.T, opts ...func(*hub.Hub)) *fleet {
	t.Helper()

	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	audit := &syncBuffer{}
	h := &hub.Hub{
		Directory: dir,
		Audit:     audit,
		Errors:    io.Discard,
		// Short enough to keep the tests quick, long enough to be real.
		KillGrace:        200 * time.Millisecond,
		PingInterval:     100 * time.Millisecond,
		PingTimeout:      2 * time.Second,
		HandshakeTimeout: 5 * time.Second,
	}
	for _, opt := range opts {
		opt(h)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- h.Serve(ctx, ln) }()

	var once sync.Once
	var serveErr error
	wait := func() error {
		once.Do(func() {
			select {
			case serveErr = <-served:
			case <-time.After(15 * time.Second):
				serveErr = errors.New("hub.Serve did not return after the context was cancelled")
			}
		})
		return serveErr
	}
	t.Cleanup(func() {
		cancel()
		if err := wait(); err != nil {
			t.Errorf("hub.Serve: %v", err)
		}
	})

	return &fleet{
		t:       t,
		dir:     dir,
		work:    t.TempDir(),
		url:     "ws://" + ln.Addr().String(),
		audit:   audit,
		stopHub: cancel,
		waitHub: wait,
	}
}

// capability writes an executable wrapper into the rpc directory.
func (f *fleet) capability(name, script string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(script), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

// dial opens a connection without the client, for the handshakes a well
// behaved client would never send.
func (f *fleet) dial(ctx context.Context) *protocol.Conn {
	f.t.Helper()
	ws, _, err := websocket.Dial(ctx, f.url, nil)
	if err != nil {
		f.t.Fatalf("dialing the hub: %v", err)
	}
	f.t.Cleanup(func() { ws.CloseNow() })
	return protocol.NewConn(ws)
}

func (f *fleet) config(sandboxID string) *config.Config {
	c := &config.Config{}
	c.Hub.URL = f.url
	c.Sandbox.ID = sandboxID
	return c
}

type result struct {
	code   int
	stdout string
	stderr string
}

// call runs a capability as the sandbox "mybox" would.
func (f *fleet) call(name string, args ...string) result {
	f.t.Helper()
	return f.callAs(context.Background(), "mybox", nil, name, args...)
}

func (f *fleet) callAs(ctx context.Context, sandboxID string, stdin io.Reader, name string, args ...string) result {
	var stdout, stderr syncBuffer
	code := rpc.Run(ctx, f.config(sandboxID), name, args, stdin, &stdout, &stderr)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// auditLines returns the log lines the hub has written so far.
func (f *fleet) auditLines() []string {
	text := strings.TrimSuffix(f.audit.String(), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// waitForAudit waits for a log line containing want, since a line is written
// after the call has ended and the client may see its result first.
func (f *fleet) waitForAudit(want string) string {
	f.t.Helper()
	var line string
	ok := waitFor(2*time.Second, func() bool {
		for _, l := range f.auditLines() {
			if strings.Contains(l, want) {
				line = l
				return true
			}
		}
		return false
	})
	if !ok {
		f.t.Fatalf("no audit line containing %q; got:\n%s", want, f.audit.String())
	}
	return line
}

func waitFor(timeout time.Duration, done func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if done() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return done()
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
