//go:build unix

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// wrapper builds a capability that starts a background child, records that
// child's pid, and then waits around — the shape of a deploy script that
// leaves something running while it works.
func (f *fleet) longRunning(name, trap string) string {
	f.t.Helper()
	pidFile := filepath.Join(f.work, name+".pid")
	f.capability(name, fmt.Sprintf(`#!/bin/sh
# af: a wrapper with a child that outlives careless parents
%s
(while true; do sleep 0.05; done) &
echo $! > %s
echo started
while true; do sleep 0.05; done
`, trap, pidFile))
	return pidFile
}

// The sandbox disappearing mid-call must take the whole process group with it,
// children included, rather than leaking it onto the host.
func TestKillsTheProcessGroupWhenTheSandboxGoesAway(t *testing.T) {
	t.Parallel()
	f := start(t)
	pidFile := f.longRunning("longrun", "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan result, 1)
	go func() { done <- f.callAs(ctx, "mybox", nil, "longrun") }()

	child := waitForPID(t, pidFile)
	cancel()

	if !waitFor(10*time.Second, func() bool { return !alive(child) }) {
		t.Fatalf("the grandchild %d outlived its caller", child)
	}
	<-done
	if line := f.waitForAudit("longrun"); !strings.Contains(line, "exit=143") {
		t.Errorf("audit line = %q, want the SIGTERM exit", line)
	}
}

// A wrapper that ignores SIGTERM gets its grace period and then SIGKILL.
func TestSigkillsAStubbornProcessGroup(t *testing.T) {
	t.Parallel()
	f := start(t)
	pidFile := f.longRunning("stubborn", "trap '' TERM")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan result, 1)
	go func() { done <- f.callAs(ctx, "mybox", nil, "stubborn") }()

	child := waitForPID(t, pidFile)
	cancel()

	if !waitFor(10*time.Second, func() bool { return !alive(child) }) {
		t.Fatalf("the grandchild %d survived SIGKILL", child)
	}
	<-done
	if line := f.waitForAudit("stubborn"); !strings.Contains(line, "exit=137") {
		t.Errorf("audit line = %q, want the SIGKILL exit", line)
	}
}

// Stopping the hub is the same promise from the other side: it does not leave
// host processes behind either.
func TestHubShutdownDoesNotLeakProcesses(t *testing.T) {
	t.Parallel()
	f := start(t)
	pidFile := f.longRunning("longrun", "")

	go f.call("longrun")
	child := waitForPID(t, pidFile)

	f.stopHub()
	if err := f.waitHub(); err != nil {
		t.Fatalf("hub.Serve: %v", err)
	}
	if !waitFor(5*time.Second, func() bool { return !alive(child) }) {
		t.Errorf("the grandchild %d outlived the hub", child)
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	ok := waitFor(10*time.Second, func() bool {
		content, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(content)))
		return err == nil && pid > 0
	})
	if !ok {
		t.Fatalf("the capability never recorded its child's pid in %s", path)
	}
	if !alive(pid) {
		t.Fatalf("the child %d was not running to begin with", pid)
	}
	return pid
}

// alive reports whether a pid is still a process. The grandchildren these
// tests watch are reparented when their wrapper dies, so they leave no zombie
// behind to confuse the answer.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
