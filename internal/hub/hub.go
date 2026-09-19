// Package hub is the host side of agentfleet: it accepts connections from
// sandboxes, runs capabilities on their behalf, and logs one line per call.
package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/severinraez/agentfleet/internal/capability"
	"github.com/severinraez/agentfleet/internal/protocol"
)

// Defaults for the hub's timings. None of them bound how long a call may run.
const (
	DefaultKillGrace        = 5 * time.Second
	DefaultPingInterval     = 30 * time.Second
	DefaultPingTimeout      = 60 * time.Second
	DefaultHandshakeTimeout = 10 * time.Second
)

// Hub serves capabilities to sandboxes. The zero value plus Directory is
// usable; the rest has defaults.
type Hub struct {
	// Directory holds the capabilities, one executable file per capability.
	Directory string
	// WorkingDirectory is where capabilities run. Defaults to the hub's own.
	WorkingDirectory string

	// Audit receives one line per call. Defaults to os.Stdout.
	Audit io.Writer
	// Errors receives the hub's own diagnostics, which are not the audit log.
	// Defaults to os.Stderr.
	Errors io.Writer

	// KillGrace is how long a wrapper has between SIGTERM and SIGKILL once
	// its caller is gone.
	KillGrace time.Duration
	// PingInterval and PingTimeout detect a sandbox that vanished without
	// closing its connection.
	PingInterval time.Duration
	PingTimeout  time.Duration
	// HandshakeTimeout bounds how long a connection may stay silent before
	// saying hello. Once a call has started, no deadline applies.
	HandshakeTimeout time.Duration

	initOnce sync.Once
	audit    *auditLog
	calls    sync.WaitGroup
}

// prefix names the hub in everything it writes, on either stream.
const prefix = "agentfleet hub: "

// init fills in every optional field, so that the rest of the hub reads them
// directly instead of each use site rediscovering the default. Every entry
// point calls it before touching anything it normalizes.
func (h *Hub) init() {
	h.initOnce.Do(func() {
		if h.Audit == nil {
			h.Audit = os.Stdout
		}
		if h.Errors == nil {
			h.Errors = os.Stderr
		}
		h.KillGrace = orDefault(h.KillGrace, DefaultKillGrace)
		h.PingInterval = orDefault(h.PingInterval, DefaultPingInterval)
		h.PingTimeout = orDefault(h.PingTimeout, DefaultPingTimeout)
		h.HandshakeTimeout = orDefault(h.HandshakeTimeout, DefaultHandshakeTimeout)
		h.audit = &auditLog{w: h.Audit}
	})
}

func orDefault(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}

// announce reports something about the hub itself on the same stream as the
// log: starting up is news for whoever is watching the calls go by, not a
// diagnostic to be filtered out.
func (h *Hub) announce(format string, args ...any) {
	h.audit.line(fmt.Sprintf(prefix+format, args...))
}

func (h *Hub) errorf(format string, args ...any) {
	fmt.Fprintf(h.Errors, prefix+format+"\n", args...)
}

// ListenAndServe binds addr and serves until ctx is done.
func (h *Hub) ListenAndServe(ctx context.Context, addr string) error {
	h.init()
	if err := h.checkDirectory(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	h.announce("listening on %s, capabilities from %s", ln.Addr(), h.Directory)
	return h.Serve(ctx, ln)
}

// Serve accepts connections on ln until ctx is done. It returns once every
// call in flight has been taken down with its connection.
func (h *Hub) Serve(ctx context.Context, ln net.Listener) error {
	h.init()
	if err := h.checkDirectory(); err != nil {
		return err
	}

	// Every request context descends from ctx, so cancelling it reaches the
	// calls in flight. That matters because an upgraded websocket is a
	// hijacked connection, and http.Server.Close does not know about those:
	// without this, stopping the hub would leave host processes running.
	srv := &http.Server{
		Handler:     h,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			srv.Close()
		case <-stop:
		}
	}()

	err := srv.Serve(ln)
	h.calls.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (h *Hub) checkDirectory() error {
	fi, err := os.Stat(h.Directory)
	if err != nil {
		return fmt.Errorf("rpc.directory: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("rpc.directory: %s is not a directory", h.Directory)
	}
	return nil
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.init()
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUpgradeRequired)
		io.WriteString(w, prefix+"this is a websocket endpoint, call it with `agentfleet rpc` from a sandbox\n")
		return
	}

	h.calls.Add(1)
	defer h.calls.Done()

	// AcceptOptions are left at their defaults on purpose: that keeps the
	// origin check on, so a browser on the host cannot be talked into using
	// its own reachability as a way into the hub.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.errorf("websocket handshake from %s failed: %v", r.RemoteAddr, err)
		return
	}
	c := protocol.NewConn(ws)
	defer c.CloseNow()

	h.handle(r.Context(), c)
}

func (h *Hub) handle(ctx context.Context, c *protocol.Conn) {
	// One reading does both jobs a log line needs: time.Time carries the wall
	// clock it is stamped with and the monotonic clock it is measured from.
	started := time.Now()

	hctx, cancel := context.WithTimeout(ctx, h.HandshakeTimeout)
	msg, err := c.Recv(hctx)
	cancel()
	if err != nil {
		h.errorf("no hello: %v", err)
		return
	}
	if msg.Kind != protocol.KindHello {
		h.fatal(ctx, c, fmt.Sprintf("expected a hello message, got %s", msg.Kind))
		return
	}
	var hello protocol.Hello
	if err := msg.JSON(&hello); err != nil {
		h.fatal(ctx, c, err.Error())
		return
	}

	if hello.Protocol != protocol.Version {
		h.reject(ctx, c, hello, started, "protocol-mismatch",
			fmt.Sprintf("protocol mismatch: sandbox speaks %q, hub speaks %q — update both sides together",
				hello.Protocol, protocol.Version))
		return
	}
	if err := protocol.ValidateSandboxID(hello.Sandbox); err != nil {
		h.reject(ctx, c, hello, started, "invalid-sandbox-id", err.Error())
		return
	}

	switch hello.Op {
	case protocol.OpList:
		h.list(ctx, c)
	case protocol.OpExec:
		h.exec(ctx, c, hello, started)
	default:
		h.reject(ctx, c, hello, started, "unknown-op", fmt.Sprintf("unknown operation %q", hello.Op))
	}

	// A closing handshake rather than a dropped connection: the sandbox may
	// still be sending stdin, and a connection reset can take the last
	// message written with it.
	c.Close("")
}

// fatal reports a failure of agentfleet itself to the sandbox, which prints it
// and exits 125. Closing the connection is left to handle, so that nothing is
// logged behind a close handshake the peer may be in no hurry to answer.
func (h *Hub) fatal(ctx context.Context, c *protocol.Conn, message string) {
	if err := c.SendJSON(ctx, protocol.KindFatal, protocol.Fatal{Message: message}); err != nil {
		h.errorf("reporting %q: %v", message, err)
	}
}

// reject refuses a call. A refused capability call is logged like any other —
// an attempt that was denied is exactly what this log is for. A refused
// listing has no capability to name, so it goes to the hub's diagnostics.
func (h *Hub) reject(ctx context.Context, c *protocol.Conn, hello protocol.Hello, started time.Time, reason, message string) {
	if hello.Op == protocol.OpExec {
		record := newRecord(hello, started)
		record.Duration = time.Since(started)
		record.Error = reason
		h.audit.write(record)
	} else {
		h.errorf("%s", message)
	}
	h.fatal(ctx, c, message)
}

func (h *Hub) list(ctx context.Context, c *protocol.Conn) {
	caps, err := capability.List(h.Directory)
	if err != nil {
		h.errorf("%v", err)
		h.fatal(ctx, c, err.Error())
		return
	}
	list := protocol.List{Capabilities: make([]protocol.Capability, 0, len(caps))}
	for _, entry := range caps {
		list.Capabilities = append(list.Capabilities, protocol.Capability{
			Name:        entry.Name,
			Description: entry.Description,
		})
	}
	if err := c.SendJSON(ctx, protocol.KindList, list); err != nil {
		h.errorf("sending the listing: %v", err)
	}
}

func (h *Hub) exec(ctx context.Context, c *protocol.Conn, hello protocol.Hello, started time.Time) {
	path, err := capability.Resolve(h.Directory, hello.Name)
	if err != nil {
		h.reject(ctx, c, hello, started, "unknown-capability", err.Error())
		return
	}

	record := newRecord(hello, started)

	exit, in, out, err := h.run(ctx, c, hello, path)
	record.In, record.Out = in, out
	record.Duration = time.Since(started)
	if err != nil {
		record.Error = "exec-failed"
	} else {
		record.Exit = exit.Status()
	}
	h.audit.write(record)
	if err != nil {
		h.errorf("%s: %v", hello.Name, err)
		h.fatal(ctx, c, err.Error())
	}
}

// run starts the capability and moves bytes until it ends, or until the
// sandbox goes away and the process group is taken down with it.
func (h *Hub) run(ctx context.Context, c *protocol.Conn, hello protocol.Hello, path string) (protocol.Exit, int64, int64, error) {
	proc, err := start(path, h.WorkingDirectory, hello.Sandbox, hello.Args)
	if err != nil {
		return protocol.Exit{}, 0, 0, err
	}

	// callCtx ends when the connection does, whether because the sandbox
	// closed it, dropped off the network, or stopped answering pings.
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-callCtx.Done():
			proc.kill(h.KillGrace)
		case <-proc.done:
		}
	}()
	go func() {
		if h.ping(callCtx, c) != nil {
			cancel()
		}
	}()

	var in, out atomic.Int64

	var pumps sync.WaitGroup
	pumps.Add(2)
	pumpOut := func(kind protocol.Kind, r io.Reader) {
		defer pumps.Done()
		out.Add(c.Pump(callCtx, kind, r))
	}
	go pumpOut(protocol.KindStdout, proc.stdout)
	go pumpOut(protocol.KindStderr, proc.stderr)

	go func() {
		// The read loop runs for the life of the connection: it feeds stdin,
		// and it is also what lets the websocket see the pongs.
		h.pumpIn(callCtx, c, proc.stdin, &in)
		cancel()
	}()

	pumps.Wait()
	exit, waitErr := proc.wait()
	if waitErr != nil {
		return exit, in.Load(), out.Load(), waitErr
	}
	if err := c.SendJSON(ctx, protocol.KindExit, exit); err != nil {
		h.errorf("%s: reporting the exit: %v", hello.Name, err)
	}
	return exit, in.Load(), out.Load(), nil
}

// pumpIn feeds the capability's stdin from the connection and keeps reading
// until the connection ends.
func (h *Hub) pumpIn(ctx context.Context, c *protocol.Conn, w io.WriteCloser, in *atomic.Int64) {
	closed := false
	closeStdin := func() {
		if !closed {
			closed = true
			w.Close()
		}
	}
	defer closeStdin()

	for {
		msg, err := c.Recv(ctx)
		if err != nil {
			return
		}
		switch msg.Kind {
		case protocol.KindStdin:
			if closed {
				continue
			}
			n, err := w.Write(msg.Data)
			in.Add(int64(n))
			if err != nil {
				// The capability is not reading its stdin any more. Stop
				// writing, but keep the connection's read loop alive.
				closeStdin()
			}
		case protocol.KindStdinEOF:
			closeStdin()
		}
	}
}

// ping notices a sandbox that disappeared without closing its connection —
// the case where TCP alone would keep a host process alive for hours. It
// reports the peer going quiet and leaves taking the call down to run, which
// is what owns the call's lifetime.
func (h *Hub) ping(ctx context.Context, c *protocol.Conn) error {
	ticker := time.NewTicker(h.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pctx, pcancel := context.WithTimeout(ctx, h.PingTimeout)
			err := c.Ping(pctx)
			pcancel()
			if err != nil && ctx.Err() == nil {
				return err
			}
		}
	}
}
