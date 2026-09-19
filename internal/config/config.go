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
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
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
	get    func(*Config) string
	set    func(*Config, string)
}

var settings = []setting{
	{
		key: "hub.listen", env: "AF_HUB_LISTEN",
		get: func(c *Config) string { return c.Hub.Listen },
		set: func(c *Config, v string) { c.Hub.Listen = v },
	},
	{
		key: "hub.url", env: "AF_HUB_URL",
		get: func(c *Config) string { return c.Hub.URL },
		set: func(c *Config, v string) { c.Hub.URL = v },
	},
	{
		key: "rpc.directory", env: "AF_RPC_DIRECTORY", isPath: true,
		get: func(c *Config) string { return c.RPC.Directory },
		set: func(c *Config, v string) { c.RPC.Directory = v },
	},
	{
		key: "rpc.working_directory", env: "AF_RPC_WORKING_DIRECTORY", isPath: true,
		get: func(c *Config) string { return c.RPC.WorkingDirectory },
		set: func(c *Config, v string) { c.RPC.WorkingDirectory = v },
	},
	{
		key: "sandbox.id", env: "AF_SANDBOX_ID",
		get: func(c *Config) string { return c.Sandbox.ID },
		set: func(c *Config, v string) { c.Sandbox.ID = v },
	},
}

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
		value := s.get(cfg)
		base := fileDir
		if v, ok := lookup(s.env); ok {
			// An empty environment variable is a deliberate empty value, not
			// a fallthrough to the file.
			value, base = v, wd
		}
		if s.isPath && value != "" && !filepath.IsAbs(value) {
			value = filepath.Clean(filepath.Join(base, value))
		}
		s.set(cfg, value)
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
	if err := c.require("hub.listen", c.Hub.Listen); err != nil {
		return err
	}
	return c.require("rpc.directory", c.RPC.Directory)
}

// RequireSandbox checks the settings the sandbox side needs.
func (c *Config) RequireSandbox() error {
	if err := c.require("hub.url", c.Hub.URL); err != nil {
		return err
	}
	if err := c.require("sandbox.id", c.Sandbox.ID); err != nil {
		return err
	}
	return ValidateSandboxID(c.Sandbox.ID)
}

func (c *Config) require(key, value string) error {
	if strings.TrimSpace(value) != "" {
		return nil
	}
	for _, s := range settings {
		if s.key == key {
			return fmt.Errorf("%s is not set (%s: %s, or %s)", key, FileName, key, s.env)
		}
	}
	return fmt.Errorf("%s is not set", key)
}

var sandboxIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateSandboxID keeps sandbox ids to something that can be an environment
// variable value and a log field without surprises. The hub applies the same
// rule to what arrives on the wire — the sandbox picks its own id, so the rule
// has to hold on the receiving side too.
func ValidateSandboxID(id string) error {
	if !sandboxIDRE.MatchString(id) {
		return fmt.Errorf("sandbox.id %q is not usable: 1-64 characters of letters, digits, dot, dash or underscore, starting with a letter or digit", id)
	}
	return nil
}
