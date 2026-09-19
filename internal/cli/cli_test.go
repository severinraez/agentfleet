package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/severinraez/agentfleet/internal/protocol"
)

// emptyConfig points the loader at a config file with nothing in it, so that
// these tests do not depend on whatever the machine running them has.
func emptyConfig(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentfleet.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_CONFIG_PATH", path)
	for _, key := range []string{"AF_HUB_LISTEN", "AF_HUB_URL", "AF_RPC_DIRECTORY", "AF_RPC_WORKING_DIRECTORY", "AF_SANDBOX_ID"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := Run(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no arguments", nil, "agentfleet rpc NAME [ARGUMENTS]"},
		{"unknown command", []string{"deploy"}, `unknown command "deploy"`},
		{"hub takes no arguments", []string{"hub", "--verbose"}, "hub takes no arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := run(t, tt.args...)
			if code != protocol.ExitAgentfleet {
				t.Errorf("exit = %d, want %d", code, protocol.ExitAgentfleet)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr, tt.want)
			}
		})
	}
}

// hub.listen has no default, so a hub that is not told where to bind has to
// say so rather than pick something.
func TestHubRefusesWithoutItsSettings(t *testing.T) {
	emptyConfig(t)

	code, _, stderr := run(t, "hub")

	if code != protocol.ExitAgentfleet {
		t.Errorf("exit = %d, want %d", code, protocol.ExitAgentfleet)
	}
	for _, want := range []string{"hub.listen", "AF_HUB_LISTEN"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to mention %q", stderr, want)
		}
	}
}

func TestRPCRefusesWithoutItsSettings(t *testing.T) {
	emptyConfig(t)

	code, _, stderr := run(t, "rpc", "deploy")

	if code != protocol.ExitAgentfleet {
		t.Errorf("exit = %d, want %d", code, protocol.ExitAgentfleet)
	}
	if !strings.Contains(stderr, "AF_HUB_URL") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRPCRefusesAnUnusableSandboxID(t *testing.T) {
	emptyConfig(t)
	t.Setenv("AF_HUB_URL", "ws://127.0.0.1:1")
	t.Setenv("AF_SANDBOX_ID", "my box")

	code, _, stderr := run(t, "rpc", "deploy")

	if code != protocol.ExitAgentfleet {
		t.Errorf("exit = %d, want %d", code, protocol.ExitAgentfleet)
	}
	if !strings.Contains(stderr, "sandbox.id") {
		t.Errorf("stderr = %q", stderr)
	}
}

// A missing rpc directory is worth refusing at startup, not at the first call.
func TestHubRefusesAMissingRPCDirectory(t *testing.T) {
	emptyConfig(t)
	t.Setenv("AF_HUB_LISTEN", "127.0.0.1:0")
	t.Setenv("AF_RPC_DIRECTORY", filepath.Join(t.TempDir(), "nope"))

	code, _, stderr := run(t, "hub")

	if code != protocol.ExitAgentfleet {
		t.Errorf("exit = %d, want %d", code, protocol.ExitAgentfleet)
	}
	if !strings.Contains(stderr, "rpc.directory") {
		t.Errorf("stderr = %q", stderr)
	}
}
