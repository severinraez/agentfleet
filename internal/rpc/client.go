// Package rpc is the sandbox side of agentfleet: it asks a hub to run a
// capability and mirrors the result, so that calling one feels like running
// the binary locally.
package rpc

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/coder/websocket"

	"github.com/severinraez/agentfleet/internal/config"
	"github.com/severinraez/agentfleet/internal/protocol"
)

// Run performs one call and returns the exit code agentfleet should exit with.
// An empty name asks for the listing instead.
//
// Signals are deliberately not trapped: if this process dies, its connection
// goes with it and the hub takes down the host process group.
func Run(ctx context.Context, cfg *config.Config, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ws, _, err := websocket.Dial(ctx, cfg.Hub.URL, nil)
	if err != nil {
		return fail(stderr, fmt.Sprintf("cannot reach the hub at %s: %v", cfg.Hub.URL, err))
	}
	c := protocol.NewConn(ws)
	defer c.CloseNow()

	op := protocol.OpExec
	if name == "" {
		op = protocol.OpList
	}
	hello := protocol.Hello{
		Protocol: protocol.Version,
		Sandbox:  cfg.Sandbox.ID,
		Op:       op,
		Name:     name,
		Args:     args,
	}
	if err := c.SendJSON(ctx, protocol.KindHello, hello); err != nil {
		return fail(stderr, fmt.Sprintf("talking to the hub at %s: %v", cfg.Hub.URL, err))
	}

	if op == protocol.OpList {
		return list(ctx, c, stdout, stderr)
	}
	return exec(ctx, c, stdin, stdout, stderr)
}

func list(ctx context.Context, c *protocol.Conn, stdout, stderr io.Writer) int {
	msg, err := c.Recv(ctx)
	if err != nil {
		return lost(stderr)
	}
	switch msg.Kind {
	case protocol.KindList:
		var l protocol.List
		if err := msg.JSON(&l); err != nil {
			return fail(stderr, err.Error())
		}
		w := tabwriter.NewWriter(stdout, 0, 0, 3, ' ', 0)
		for _, entry := range l.Capabilities {
			if entry.Description == "" {
				fmt.Fprintln(w, entry.Name)
				continue
			}
			fmt.Fprintf(w, "%s\t%s\n", entry.Name, entry.Description)
		}
		w.Flush()
		return 0
	case protocol.KindFatal:
		return fatal(stderr, msg)
	default:
		return fail(stderr, fmt.Sprintf("expected a listing, got %s", msg.Kind))
	}
}

func exec(ctx context.Context, c *protocol.Conn, stdin io.Reader, stdout, stderr io.Writer) int {
	if stdin != nil {
		go pumpStdin(ctx, c, stdin)
	}
	for {
		msg, err := c.Recv(ctx)
		if err != nil {
			return lost(stderr)
		}
		switch msg.Kind {
		case protocol.KindStdout:
			stdout.Write(msg.Data)
		case protocol.KindStderr:
			stderr.Write(msg.Data)
		case protocol.KindExit:
			var exit protocol.Exit
			if err := msg.JSON(&exit); err != nil {
				return fail(stderr, err.Error())
			}
			return exit.Status()
		case protocol.KindFatal:
			return fatal(stderr, msg)
		}
	}
}

func pumpStdin(ctx context.Context, c *protocol.Conn, r io.Reader) {
	c.Pump(ctx, protocol.KindStdin, r)
	c.Send(ctx, protocol.KindStdinEOF, nil)
}

func fatal(stderr io.Writer, msg protocol.Message) int {
	var f protocol.Fatal
	if err := msg.JSON(&f); err != nil {
		return fail(stderr, err.Error())
	}
	return fail(stderr, f.Message)
}

func fail(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "agentfleet: %s\n", message)
	return protocol.ExitAgentfleet
}

func lost(stderr io.Writer) int {
	return fail(stderr, "the connection to the hub was lost")
}
