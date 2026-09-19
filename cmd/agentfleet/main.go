// Command agentfleet is both halves of agentfleet: the hub that runs
// capabilities on a host, and the rpc client that calls them from a sandbox.
package main

import (
	"os"

	"github.com/severinraez/agentfleet/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
