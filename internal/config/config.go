// Package config loads agentfleet's configuration from a YAML file and the
// environment, where the environment takes precedence. There are no command
// line flags.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/severinraez/agentfleet/internal/protocol"
)

// Config holds every setting, for both roles. A hub uses hub.listen and the
// rpc.* settings; a sandbox uses hub.url and sandbox.id.
type Config struct {
	Hub     Hub     `yaml:"hub"`
	RPC     RPC     `yaml:"rpc"`
	Sandbox Sandbox `yaml:"sandbox"`
}

type Hub struct {
	Listen string `yaml:"listen"`
	URL    string `yaml:"url"`
}

type RPC struct {
	Directory        string `yaml:"directory"`
	WorkingDirectory string `yaml:"working_directory"`
}

type Sandbox struct {
	ID string `yaml:"id"`
}

// EnvConfigPath names the config file explicitly, overriding the search.
const EnvConfigPath = "AF_CONFIG_PATH"

// FileName is the config file looked for in the working directory and, dotted,
// in the home directory.
const FileName = "agentfleet.yaml"

// setting ties a config key to its environment variable. The two spellings
// appear together in error messages, because which one is missing is rarely
// obvious from one side alone.
type setting struct {
	key    string
	env    string
	isPath bool
	// field addresses the setting in a Config, for both reading and writing:
	// one accessor cannot name a different field on the way in than on the
	// way out.
	field func(*Config) *string
}

var (
	settingHubListen = setting{
		key: "hub.listen", env: "AF_HUB_LISTEN",
		field: func(c *Config) *string { return &c.Hub.Listen },
	}
	settingHubURL = setting{
		key: "hub.url", env: "AF_HUB_URL",
		field: func(c *Config) *string { return &c.Hub.URL },
	}
	settingRPCDirectory = setting{
		key: "rpc.directory", env: "AF_RPC_DIRECTORY", isPath: true,
		field: func(c *Config) *string { return &c.RPC.Directory },
	}
	settingRPCWorkingDirectory = setting{
		key: "rpc.working_directory", env: "AF_RPC_WORKING_DIRECTORY", isPath: true,
		field: func(c *Config) *string { return &c.RPC.WorkingDirectory },
	}
	settingSandboxID = setting{
		key: "sandbox.id", env: "AF_SANDBOX_ID",
		field: func(c *Config) *string { return &c.Sandbox.ID },
	}

	settings = []setting{
		settingHubListen,
		settingHubURL,
		settingRPCDirectory,
		settingRPCWorkingDirectory,
		settingSandboxID,
	}
)

// Loader resolves configuration. The zero value reads the real environment and
// the real working directory; tests fill the fields in instead.
type Loader struct {
	// LookupEnv defaults to os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// WorkingDir defaults to the process working directory.
	WorkingDir string
	// HomeDir defaults to the user's home directory.
	HomeDir string
}

// Load resolves configuration with the process environment.
func Load() (*Config, error) { return Loader{}.Load() }

// Load reads the config file, overlays the environment and resolves paths.
//
// Paths from the file resolve against the file's directory; paths from the
// environment resolve against the working directory.
func (l Loader) Load() (*Config, error) {
	lookup := l.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	wd := l.WorkingDir
	if wd == "" {
		var err error
		if wd, err = os.Getwd(); err != nil {
			return nil, fmt.Errorf("determining the working directory: %w", err)
		}
	}

	path, err := l.findFile(lookup, wd)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if path != "" {
		if err := readFile(path, cfg); err != nil {
			return nil, err
		}
	}
	fileDir := wd
	if path != "" {
		fileDir = filepath.Dir(path)
	}

	for _, s := range settings {
		value := s.field(cfg)
		base := fileDir
		if v, ok := lookup(s.env); ok {
			// An empty environment variable is a deliberate empty value, not
			// a fallthrough to the file.
			*value, base = v, wd
		}
		if s.isPath && *value != "" && !filepath.IsAbs(*value) {
			*value = filepath.Clean(filepath.Join(base, *value))
		}
	}
	return cfg, nil
}

// findFile applies the documented search: AF_CONFIG_PATH, then ./agentfleet.yaml,
// then ~/.agentfleet.yaml. A path given explicitly must exist — pointing at a
// file that is not there is a mistake worth reporting, not worth ignoring.
func (l Loader) findFile(lookup func(string) (string, bool), wd string) (string, error) {
	if p, ok := lookup(EnvConfigPath); ok && p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(wd, p)
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s points at %s: %w", EnvConfigPath, p, err)
		}
		return p, nil
	}
	if p := filepath.Join(wd, FileName); exists(p) {
		return p, nil
	}
	home := l.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		if p := filepath.Join(home, "."+FileName); exists(p) {
			return p, nil
		}
	}
	return "", nil
}

func exists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func readFile(path string, cfg *Config) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()

	// Strict: an unknown key is a typo, and a typo that silently does nothing
	// is exactly the failure this program cannot afford.
	dec := yaml.NewDecoder(f, yaml.DisallowUnknownField())
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	return nil
}

// RequireHub checks the settings the host side needs.
func (c *Config) RequireHub() error {
	if err := c.require(settingHubListen); err != nil {
		return err
	}
	return c.require(settingRPCDirectory)
}

// RequireSandbox checks the settings the sandbox side needs.
func (c *Config) RequireSandbox() error {
	if err := c.require(settingHubURL); err != nil {
		return err
	}
	if err := c.require(settingSandboxID); err != nil {
		return err
	}
	return ValidateSandboxID(c.Sandbox.ID)
}

// require reports a missing setting under both its spellings, because which
// one the reader is looking for is rarely obvious from one side alone.
func (c *Config) require(s setting) error {
	if strings.TrimSpace(*s.field(c)) != "" {
		return nil
	}
	return fmt.Errorf("%s is not set (%s: %s, or %s)", s.key, FileName, s.key, s.env)
}

// ValidateSandboxID keeps sandbox ids to something that can be an environment
// variable value and a log field without surprises. It is the wire rule, from
// protocol, because the hub checks the very same id on the receiving side.
func ValidateSandboxID(id string) error { return protocol.ValidateSandboxID(id) }
