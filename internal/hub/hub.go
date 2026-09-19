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
	"github.com/severinraez/agentfleet/internal/config"
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

	// Now defaults to time.Now and supplies the timestamp of a log line.
	Now func() time.Time

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

func (h *Hub) init() {
	h.initOnce.Do(func() {
		out := h.Audit
		if out == nil {
			out = os.Stdout
		}
		h.audit = &auditLog{w: out}
	})
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
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
	h.audit.line(fmt.Sprintf("agentfleet hub: "+format, args...))
}

func (h *Hub) errorf(format string, args ...any) {
	w := h.Errors
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, "agentfleet hub: "+format+"\n", args...)
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
		io.WriteString(w, "agentfleet hub: this is a websocket endpoint, call it with `agentfleet rpc` from a sandbox\n")
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

// call carries the two clocks a log line needs: the wall time it is stamped
// with, and the monotonic start it is measured from.
type call struct {
	at    time.Time
	since time.Time
}

func (c call) elapsed() time.Duration { return time.Since(c.since) }

func (h *Hub) handle(ctx context.Context, c *protocol.Conn) {
	started := call{at: h.now(), since: time.Now()}

	hctx, cancel := context.WithTimeout(ctx, orDefault(h.HandshakeTimeout, DefaultHandshakeTimeout))
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
	if err := config.ValidateSandboxID(hello.Sandbox); err != nil {
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
func (h *Hub) reject(ctx context.Context, c *protocol.Conn, hello protocol.Hello, started call, reason, message string) {
	if hello.Op == protocol.OpExec {
		h.audit.write(Record{
			Time:      started.at,
			SandboxID: hello.Sandbox,
			Name:      hello.Name,
			Args:      hello.Args,
			Exit:      protocol.ExitAgentfleet,
			Duration:  started.elapsed(),
			Error:     reason,
		})
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

func (h *Hub) exec(ctx context.Context, c *protocol.Conn, hello protocol.Hello, started call) {
	path, err := capability.Resolve(h.Directory, hello.Name)
	if err != nil {
		h.reject(ctx, c, hello, started, "unknown-capability", err.Error())
		return
	}

	record := Record{
		Time:      started.at,
		SandboxID: hello.Sandbox,
		Name:      hello.Name,
		Args:      hello.Args,
		Exit:      protocol.ExitAgentfleet,
	}

	exit, in, out, err := h.run(ctx, c, hello, path)
	record.In, record.Out = in, out
	record.Duration = started.elapsed()
	switch {
	case err != nil:
		record.Error = "exec-failed"
	case exit.Signal != 0:
		record.Exit = 128 + exit.Signal
	default:
		record.Exit = exit.Code
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
	workDir := h.WorkingDirectory

	proc, err := start(path, workDir, hello.Sandbox, hello.Args)
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
			proc.kill(orDefault(h.KillGrace, DefaultKillGrace))
		case <-proc.done:
		}
	}()
	go h.ping(callCtx, c, cancel)

	var in, out atomic.Int64

	var pumps sync.WaitGroup
	pumps.Add(2)
	go pumpOut(callCtx, c, protocol.KindStdout, proc.stdout, &out, &pumps)
	go pumpOut(callCtx, c, protocol.KindStderr, proc.stderr, &out, &pumps)

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

// pumpOut forwards one of the capability's output streams to the sandbox.
func pumpOut(ctx context.Context, c *protocol.Conn, kind protocol.Kind, r io.Reader, out *atomic.Int64, wg *sync.WaitGroup) {
	defer wg.Done()
	buf := make([]byte, protocol.ChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out.Add(int64(n))
			if err := c.Send(ctx, kind, buf[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
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
// the case where TCP alone would keep a host process alive for hours.
func (h *Hub) ping(ctx context.Context, c *protocol.Conn, cancel context.CancelFunc) {
	ticker := time.NewTicker(orDefault(h.PingInterval, DefaultPingInterval))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pctx, pcancel := context.WithTimeout(ctx, orDefault(h.PingTimeout, DefaultPingTimeout))
			err := c.Ping(pctx)
			pcancel()
			if err != nil && ctx.Err() == nil {
				cancel()
				return
			}
		}
	}
}
