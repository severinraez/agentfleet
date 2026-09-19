// Package cli turns a command line into one of agentfleet's two roles.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/severinraez/agentfleet/internal/config"
	"github.com/severinraez/agentfleet/internal/hub"
	"github.com/severinraez/agentfleet/internal/protocol"
	"github.com/severinraez/agentfleet/internal/rpc"
)

const usage = `agentfleet — a narrow, audited door from a sandbox back to its host

  agentfleet hub                     run the hub on the host
  agentfleet rpc                     list what this sandbox can call
  agentfleet rpc NAME [ARGUMENTS]    run a capability on the host

Configuration comes from agentfleet.yaml and AF_* environment variables.
There are no command line flags: everything after NAME belongs to the
capability and is passed through untouched.
`

// Run executes one command line and returns the process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return protocol.ExitAgentfleet
	}

	switch args[0] {
	case "hub":
		if len(args) > 1 {
			return fail(stderr, fmt.Sprintf("hub takes no arguments, got %q", args[1]))
		}
		return runHub(stdout, stderr)
	case "rpc":
		return runRPC(args[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "agentfleet: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return protocol.ExitAgentfleet
	}
}

func runHub(stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		return fail(stderr, err.Error())
	}
	if err := cfg.RequireHub(); err != nil {
		return fail(stderr, err.Error())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h := &hub.Hub{
		Directory:        cfg.RPC.Directory,
		WorkingDirectory: cfg.RPC.WorkingDirectory,
		Audit:            stdout,
		Errors:           stderr,
	}
	if err := h.ListenAndServe(ctx, cfg.Hub.Listen); err != nil {
		return fail(stderr, err.Error())
	}
	return 0
}

func runRPC(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		return fail(stderr, err.Error())
	}
	if err := cfg.RequireSandbox(); err != nil {
		return fail(stderr, err.Error())
	}

	var name string
	var rest []string
	if len(args) > 0 {
		name, rest = args[0], args[1:]
	}
	return rpc.Run(context.Background(), cfg, name, rest, stdin, stdout, stderr)
}

func fail(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "agentfleet: %s\n", message)
	return protocol.ExitAgentfleet
}
