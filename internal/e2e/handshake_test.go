package e2e

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/severinraez/agentfleet/internal/hub"
	"github.com/severinraez/agentfleet/internal/protocol"
)

// hello sends a handshake by hand and returns the hub's first answer.
func (f *fleet) hello(h protocol.Hello) protocol.Message {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := f.dial(ctx)
	if err := c.SendJSON(ctx, protocol.KindHello, h); err != nil {
		f.t.Fatalf("sending the hello: %v", err)
	}
	msg, err := c.Recv(ctx)
	if err != nil {
		f.t.Fatalf("reading the answer: %v", err)
	}
	return msg
}

func fatalMessage(t *testing.T, msg protocol.Message) string {
	t.Helper()
	if msg.Kind != protocol.KindFatal {
		t.Fatalf("got a %s message, want fatal", msg.Kind)
	}
	var f protocol.Fatal
	if err := msg.JSON(&f); err != nil {
		t.Fatal(err)
	}
	return f.Message
}

// Skew between an updated hub and a stale sandbox image has to fail loudly,
// naming both sides, rather than failing strangely later on.
func TestProtocolMismatchNamesBothVersions(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("deploy", "#!/bin/sh\necho ok\n")

	message := fatalMessage(t, f.hello(protocol.Hello{
		Protocol: "agentfleet/2", Sandbox: "mybox", Op: protocol.OpExec, Name: "deploy",
	}))

	for _, want := range []string{"protocol mismatch", `"agentfleet/2"`, `"` + protocol.Version + `"`} {
		if !strings.Contains(message, want) {
			t.Errorf("message = %q, want it to mention %q", message, want)
		}
	}
	f.waitForAudit("error=protocol-mismatch")
}

// The sandbox picks its own id, so the hub applies the same rule to what
// arrives on the wire as the sandbox's own config would.
func TestRejectsAnUnusableSandboxID(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("deploy", "#!/bin/sh\necho ok\n")

	message := fatalMessage(t, f.hello(protocol.Hello{
		Protocol: protocol.Version, Sandbox: "box one\nsecond line", Op: protocol.OpExec, Name: "deploy",
	}))

	if !strings.Contains(message, "sandbox.id") {
		t.Errorf("message = %q", message)
	}
	line := f.waitForAudit("error=invalid-sandbox-id")
	if strings.Count(line, "\n") != 0 || !strings.Contains(line, `"box one\nsecond line"`) {
		t.Errorf("audit line = %q, want the id quoted onto one line", line)
	}
}

func TestRejectsANonHelloFirstMessage(t *testing.T) {
	t.Parallel()
	f := start(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := f.dial(ctx)
	if err := c.Send(ctx, protocol.KindStdin, []byte("surprise")); err != nil {
		t.Fatal(err)
	}
	msg, err := c.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if message := fatalMessage(t, msg); !strings.Contains(message, "expected a hello message, got stdin") {
		t.Errorf("message = %q", message)
	}
}

func TestRejectsAnUnknownOperation(t *testing.T) {
	t.Parallel()
	f := start(t)

	message := fatalMessage(t, f.hello(protocol.Hello{
		Protocol: protocol.Version, Sandbox: "mybox", Op: "sudo",
	}))
	if !strings.Contains(message, `unknown operation "sudo"`) {
		t.Errorf("message = %q", message)
	}
}

// A connection that says nothing must not hold a goroutine forever. This is a
// handshake deadline, not a call timeout: once a call starts, it may run for
// as long as it likes.
func TestASilentConnectionIsDropped(t *testing.T) {
	t.Parallel()
	f := start(t, func(h *hub.Hub) { h.HandshakeTimeout = 200 * time.Millisecond })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := f.dial(ctx)

	if _, err := c.Recv(ctx); err == nil {
		t.Fatal("the hub kept a silent connection")
	}
}

// Curling the hub should explain itself rather than look broken.
func TestPlainHTTPGetExplainsItself(t *testing.T) {
	t.Parallel()
	f := start(t)

	resp, err := http.Get(strings.Replace(f.url, "ws://", "http://", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUpgradeRequired)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "websocket endpoint") {
		t.Errorf("body = %q", body)
	}
}

func TestUnreachableHub(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.stopHub()
	if err := f.waitHub(); err != nil {
		t.Fatal(err)
	}

	r := f.call("deploy")

	if r.code != protocol.ExitAgentfleet {
		t.Errorf("exit = %d, want %d", r.code, protocol.ExitAgentfleet)
	}
	if !strings.Contains(r.stderr, "cannot reach the hub at ws://") {
		t.Errorf("stderr = %q", r.stderr)
	}
}
