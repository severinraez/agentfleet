package hub

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/severinraez/agentfleet/internal/protocol"
)

// process is a running capability together with the process group it owns.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	// done is closed once cmd.Wait has returned. mu guards the closing, so
	// that a signal is never sent to a process group whose leader has already
	// been reaped and whose pid could be reused.
	mu   sync.Mutex
	done chan struct{}
}

// start runs a capability with the hub's environment plus the caller's name
// and the address it called from.
func start(path, workDir, sandboxID, peer string, args []string) (*process, error) {
	cmd := exec.Command(path, args...)
	cmd.Dir = workDir

	// The hub's environment, credentials and all, plus AF_SANDBOX_ID and
	// AF_PEER_ADDR. Nothing from the wire ever becomes an environment variable
	// — that is what keeps LD_PRELOAD, BASH_ENV and GIT_SSH_COMMAND from
	// turning every capability into arbitrary host execution. AF_PEER_ADDR is
	// the connection's own remote address, so it is the one value here a
	// sandbox cannot pick for itself, and the only reason to pass it.
	cmd.Env = append(os.Environ(), "AF_SANDBOX_ID="+sandboxID, "AF_PEER_ADDR="+peer)

	// Its own process group, so a wrapper's children go down with it instead
	// of being leaked when the sandbox disappears.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	p := &process{cmd: cmd, done: make(chan struct{})}
	var err error
	if p.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if p.stdout, err = cmd.StdoutPipe(); err != nil {
		return nil, err
	}
	if p.stderr, err = cmd.StderrPipe(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", path, err)
	}
	return p, nil
}

// wait reaps the process and reports the ending as the sandbox will see it.
func (p *process) wait() (protocol.Exit, error) {
	err := p.cmd.Wait()

	p.mu.Lock()
	close(p.done)
	p.mu.Unlock()

	if err == nil {
		return protocol.Exit{Code: 0}, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return protocol.Exit{Signal: int(status.Signal())}, nil
		}
		return protocol.Exit{Code: ee.ExitCode()}, nil
	}
	return protocol.Exit{Code: protocol.ExitAgentfleet}, err
}

// kill takes down the whole process group: SIGTERM, a grace period in which a
// wrapper can clean up after itself, then SIGKILL.
func (p *process) kill(grace time.Duration) {
	if !p.signal(syscall.SIGTERM) {
		return
	}
	select {
	case <-p.done:
		return
	case <-time.After(grace):
	}
	p.signal(syscall.SIGKILL)
}

// signal sends sig to the process group, unless the process has been reaped.
// It reports whether the process was still ours to signal.
func (p *process) signal(sig syscall.Signal) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done: // already reaped
		return false
	default:
	}
	// The negative pid addresses the group; Setpgid made the child its leader,
	// so the group id is its pid.
	_ = syscall.Kill(-p.cmd.Process.Pid, sig)
	return true
}
