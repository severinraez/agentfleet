package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envOf turns a map into a LookupEnv, so that config tests need neither the
// process environment nor t.Setenv and can run in parallel.
func envOf(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// files are written relative to a fresh temp root; "wd" and "home"
		// are subdirectories of it.
		files map[string]string
		env   map[string]string
		check func(t *testing.T, root string, c *Config)
	}{
		{
			name:  "reads the working directory config file",
			files: map[string]string{"wd/agentfleet.yaml": "hub:\n  listen: 192.168.100.1:7777\nrpc:\n  directory: rpc\n"},
			check: func(t *testing.T, root string, c *Config) {
				if c.Hub.Listen != "192.168.100.1:7777" {
					t.Errorf("hub.listen = %q", c.Hub.Listen)
				}
				if want := filepath.Join(root, "wd", "rpc"); c.RPC.Directory != want {
					t.Errorf("rpc.directory = %q, want %q", c.RPC.Directory, want)
				}
			},
		},
		{
			name:  "the environment wins over the file",
			files: map[string]string{"wd/agentfleet.yaml": "hub:\n  listen: from-file\n"},
			env:   map[string]string{"AF_HUB_LISTEN": "from-env"},
			check: func(t *testing.T, _ string, c *Config) {
				if c.Hub.Listen != "from-env" {
					t.Errorf("hub.listen = %q", c.Hub.Listen)
				}
			},
		},
		{
			name:  "an empty environment variable is a deliberate empty value",
			files: map[string]string{"wd/agentfleet.yaml": "hub:\n  listen: from-file\n"},
			env:   map[string]string{"AF_HUB_LISTEN": ""},
			check: func(t *testing.T, _ string, c *Config) {
				if c.Hub.Listen != "" {
					t.Errorf("hub.listen = %q, want empty", c.Hub.Listen)
				}
			},
		},
		{
			name:  "paths from the environment resolve against the working directory",
			files: map[string]string{"wd/agentfleet.yaml": "rpc:\n  directory: from-file\n"},
			env:   map[string]string{"AF_RPC_DIRECTORY": "from-env"},
			check: func(t *testing.T, root string, c *Config) {
				if want := filepath.Join(root, "wd", "from-env"); c.RPC.Directory != want {
					t.Errorf("rpc.directory = %q, want %q", c.RPC.Directory, want)
				}
			},
		},
		{
			name: "paths from a config file resolve against that file",
			files: map[string]string{
				"elsewhere/agentfleet.yaml": "rpc:\n  directory: rpc\n  working_directory: ../work\n",
			},
			env: map[string]string{"AF_CONFIG_PATH": "../elsewhere/agentfleet.yaml"},
			check: func(t *testing.T, root string, c *Config) {
				if want := filepath.Join(root, "elsewhere", "rpc"); c.RPC.Directory != want {
					t.Errorf("rpc.directory = %q, want %q", c.RPC.Directory, want)
				}
				if want := filepath.Join(root, "work"); c.RPC.WorkingDirectory != want {
					t.Errorf("rpc.working_directory = %q, want %q", c.RPC.WorkingDirectory, want)
				}
			},
		},
		{
			name:  "absolute paths are left alone",
			files: map[string]string{"wd/agentfleet.yaml": "rpc:\n  directory: /srv/rpc\n"},
			check: func(t *testing.T, _ string, c *Config) {
				if c.RPC.Directory != "/srv/rpc" {
					t.Errorf("rpc.directory = %q", c.RPC.Directory)
				}
			},
		},
		{
			name:  "falls back to the home directory",
			files: map[string]string{"home/.agentfleet.yaml": "sandbox:\n  id: fromhome\n"},
			check: func(t *testing.T, _ string, c *Config) {
				if c.Sandbox.ID != "fromhome" {
					t.Errorf("sandbox.id = %q", c.Sandbox.ID)
				}
			},
		},
		{
			name: "the working directory file wins over the home directory",
			files: map[string]string{
				"wd/agentfleet.yaml":    "sandbox:\n  id: fromwd\n",
				"home/.agentfleet.yaml": "sandbox:\n  id: fromhome\n",
			},
			check: func(t *testing.T, _ string, c *Config) {
				if c.Sandbox.ID != "fromwd" {
					t.Errorf("sandbox.id = %q", c.Sandbox.ID)
				}
			},
		},
		{
			name: "works with no config file at all",
			env:  map[string]string{"AF_HUB_URL": "ws://hub:7777", "AF_SANDBOX_ID": "mybox"},
			check: func(t *testing.T, _ string, c *Config) {
				if err := c.RequireSandbox(); err != nil {
					t.Errorf("RequireSandbox: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			wd := filepath.Join(root, "wd")
			home := filepath.Join(root, "home")
			for _, dir := range []string{wd, home} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for name, content := range tt.files {
				write(t, filepath.Join(root, name), content)
			}
			cfg, err := Loader{LookupEnv: envOf(tt.env), WorkingDir: wd, HomeDir: home}.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tt.check(t, root, cfg)
		})
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
		env   map[string]string
		want  string
	}{
		{
			name: "an explicit config path must exist",
			env:  map[string]string{"AF_CONFIG_PATH": "nope.yaml"},
			want: "AF_CONFIG_PATH points at",
		},
		{
			name:  "an unknown key is a typo, not a no-op",
			files: map[string]string{"wd/agentfleet.yaml": "hub:\n  lisen: 1.2.3.4:7777\n"},
			want:  "lisen",
		},
		{
			name:  "malformed yaml is reported with its file",
			files: map[string]string{"wd/agentfleet.yaml": "hub: [\n"},
			want:  "agentfleet.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			wd := filepath.Join(root, "wd")
			if err := os.MkdirAll(wd, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, content := range tt.files {
				write(t, filepath.Join(root, name), content)
			}
			_, err := Loader{LookupEnv: envOf(tt.env), WorkingDir: wd, HomeDir: root}.Load()
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// A missing setting has to name both spellings: which one is missing is rarely
// obvious from one side alone.
func TestRequireNamesBothSpellings(t *testing.T) {
	t.Parallel()

	hub := &Config{}
	err := hub.RequireHub()
	if err == nil {
		t.Fatal("RequireHub succeeded on an empty config")
	}
	for _, want := range []string{"hub.listen", "AF_HUB_LISTEN", FileName} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("RequireHub error %q does not mention %q", err, want)
		}
	}

	hub.Hub.Listen = "127.0.0.1:7777"
	err = hub.RequireHub()
	if err == nil || !strings.Contains(err.Error(), "AF_RPC_DIRECTORY") {
		t.Errorf("RequireHub error = %v, want it to mention AF_RPC_DIRECTORY", err)
	}

	box := &Config{}
	err = box.RequireSandbox()
	if err == nil || !strings.Contains(err.Error(), "AF_HUB_URL") {
		t.Errorf("RequireSandbox error = %v, want it to mention AF_HUB_URL", err)
	}
}

func TestValidateSandboxID(t *testing.T) {
	t.Parallel()

	valid := []string{"mybox", "box-1", "a", "A.b_c-1", strings.Repeat("a", 64)}
	invalid := []string{
		"",
		"-leading-dash",
		".leading-dot",
		"has space",
		"has\nnewline",
		"has/slash",
		"quote\"d",
		strings.Repeat("a", 65),
	}

	for _, id := range valid {
		if err := ValidateSandboxID(id); err != nil {
			t.Errorf("ValidateSandboxID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range invalid {
		if err := ValidateSandboxID(id); err == nil {
			t.Errorf("ValidateSandboxID(%q) = nil, want an error", id)
		}
	}
}
