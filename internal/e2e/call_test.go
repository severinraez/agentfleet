package e2e

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/severinraez/agentfleet/internal/protocol"
)

func TestMirrorsStreamsAndExitCode(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("greet", "#!/bin/sh\n# af: say hello\necho out\necho err >&2\nexit 3\n")

	r := f.call("greet")

	if r.code != 3 {
		t.Errorf("exit = %d, want 3", r.code)
	}
	if r.stdout != "out\n" {
		t.Errorf("stdout = %q", r.stdout)
	}
	if r.stderr != "err\n" {
		t.Errorf("stderr = %q", r.stderr)
	}
	if line := f.waitForAudit("greet"); !strings.Contains(line, "mybox greet exit=3") {
		t.Errorf("audit line = %q", line)
	}
}

// Everything after the capability name belongs to the capability. agentfleet
// never parses it, whatever it looks like.
func TestPassesArgumentsThroughUntouched(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("args", "#!/bin/sh\nfor a in \"$@\"; do echo \"[$a]\"; done\n")

	r := f.call("args", "--force", "-x", "--", "two words", "")

	want := "[--force]\n[-x]\n[--]\n[two words]\n[]\n"
	if r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
	// And the log stays unambiguous about what was passed.
	line := f.waitForAudit("args")
	if !strings.Contains(line, `args --force -x -- "two words" "" exit=0`) {
		t.Errorf("audit line = %q", line)
	}
}

func TestStreamsStdin(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("echo-stdin", "#!/bin/sh\nexec cat\n")

	// Comfortably more than one chunk, so the streaming is real.
	payload := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 120_000)

	r := f.callAs(context.Background(), "mybox", bytes.NewReader(payload), "echo-stdin")

	if r.code != 0 {
		t.Fatalf("exit = %d, stderr = %q", r.code, r.stderr)
	}
	if r.stdout != string(payload) {
		t.Errorf("stdout is %d bytes, want %d", len(r.stdout), len(payload))
	}
	line := f.waitForAudit("echo-stdin")
	if !strings.Contains(line, "in=5.3MB") || !strings.Contains(line, "out=5.3MB") {
		t.Errorf("audit line = %q, want it to account for both directions", line)
	}
}

// Secrets go on stdin, so stdin has to reach a capability that reads it after
// doing something else first.
func TestStdinReachesALateReader(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("read-secret", "#!/bin/sh\necho ready\nread secret\necho \"got:$secret\"\n")

	r := f.callAs(context.Background(), "mybox", strings.NewReader("hunter2\n"), "read-secret")

	if r.code != 0 {
		t.Fatalf("exit = %d, stderr = %q", r.code, r.stderr)
	}
	if want := "ready\ngot:hunter2\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
}

// A capability that ignores its stdin must not wedge the call.
func TestStdinIsIgnorableWithoutHanging(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("deaf", "#!/bin/sh\necho done\n")

	r := f.callAs(context.Background(), "mybox", bytes.NewReader(bytes.Repeat([]byte("x"), 2<<20)), "deaf")

	if r.code != 0 || r.stdout != "done\n" {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", r.code, r.stdout, r.stderr)
	}
}

func TestUnknownCapability(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("deploy", "#!/bin/sh\necho ok\n")

	for _, name := range []string{"nosuch", "../../etc/passwd", "deploy;id"} {
		r := f.call(name)
		if r.code != protocol.ExitAgentfleet {
			t.Errorf("%q: exit = %d, want %d", name, r.code, protocol.ExitAgentfleet)
		}
		if !strings.Contains(r.stderr, "unknown capability") {
			t.Errorf("%q: stderr = %q", name, r.stderr)
		}
	}
	// A refused attempt is logged, which is the point of logging at all.
	f.waitForAudit("error=unknown-capability")
}

func TestListing(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("deploy", "#!/bin/sh\n# af: deploy the current branch to staging\n")
	f.capability("notify", "#!/bin/sh\n# af: send a message to the #agents channel\n")
	f.capability("bare", "#!/bin/sh\necho no description\n")

	r := f.call("")

	if r.code != 0 {
		t.Fatalf("exit = %d, stderr = %q", r.code, r.stderr)
	}
	want := "bare\n" +
		"deploy   deploy the current branch to staging\n" +
		"notify   send a message to the #agents channel\n"
	if r.stdout != want {
		t.Errorf("listing =\n%q\nwant\n%q", r.stdout, want)
	}
	// A listing is not a capability call, so it writes no audit line.
	if lines := f.auditLines(); len(lines) != 0 {
		t.Errorf("listing wrote audit lines: %v", lines)
	}
}

// The wrapper gets the hub's environment plus the caller's name and the
// address it called from, and nothing else the sandbox chose.
func TestCapabilityEnvironment(t *testing.T) {
	t.Setenv("AF_TEST_HOST_SECRET", "from-the-host")
	f := start(t)
	f.capability("env", "#!/bin/sh\necho \"id=$AF_SANDBOX_ID\"\necho \"secret=$AF_TEST_HOST_SECRET\"\necho \"url=$AF_HUB_URL\"\necho \"peer=$AF_PEER_ADDR\"\n")

	r := f.callAs(context.Background(), "box-7", nil, "env")

	// The port is whatever the kernel handed the client, so only the address
	// half of AF_PEER_ADDR is pinned — that half is what a capability
	// allowlists on.
	want := "id=box-7\nsecret=from-the-host\nurl=\npeer=127.0.0.1:"
	if !strings.HasPrefix(r.stdout, want) {
		t.Errorf("stdout = %q, want it to start with %q", r.stdout, want)
	}
}

func TestSignalledCapabilityBecomes128Plus(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("selfkill", "#!/bin/sh\nkill -9 $$\n")

	r := f.call("selfkill")

	if r.code != 137 {
		t.Errorf("exit = %d, want 137", r.code)
	}
	if line := f.waitForAudit("selfkill"); !strings.Contains(line, "exit=137") {
		t.Errorf("audit line = %q", line)
	}
}

func TestConcurrentCalls(t *testing.T) {
	t.Parallel()
	f := start(t)
	f.capability("echo-id", "#!/bin/sh\necho \"$AF_SANDBOX_ID\"\n")

	const calls = 20
	var wg sync.WaitGroup
	results := make([]result, calls)
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = f.callAs(context.Background(), fmt.Sprintf("box-%d", i), nil, "echo-id")
		}()
	}
	wg.Wait()

	for i, r := range results {
		if want := fmt.Sprintf("box-%d\n", i); r.stdout != want {
			t.Errorf("call %d: stdout = %q, want %q", i, r.stdout, want)
		}
	}
	for i := range calls {
		f.waitForAudit(fmt.Sprintf("box-%d echo-id exit=0", i))
	}
	if lines := f.auditLines(); len(lines) != calls {
		t.Errorf("got %d audit lines, want %d", len(lines), calls)
	}
}
