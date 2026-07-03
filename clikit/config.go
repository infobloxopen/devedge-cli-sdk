package clikit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the on-disk CLI configuration: a set of named [Profile]s and the
// currently-selected one. It is loaded from
// $XDG_CONFIG_HOME/<app>/config.yaml (falling back to ~/.config/<app>), then
// overlaid by environment variables and flags.
type Config struct {
	CurrentProfile string             `yaml:"current_profile,omitempty"`
	Profiles       map[string]Profile `yaml:"profiles,omitempty"`
}

// Profile is one named target: a server base URL plus its auth settings. The
// active profile carries everything a command needs to reach a service.
type Profile struct {
	Name   string     `yaml:"-"`
	Server string     `yaml:"server,omitempty"`
	Auth   AuthConfig `yaml:"auth,omitempty"`
}

// AuthConfig selects how a [Profile] authenticates.
type AuthConfig struct {
	// Type is "oidc", "stub", or "" (none). The shell's SessionFactory decides
	// how to interpret it; clikit itself only wires the "stub" fast path via
	// the --dev flag.
	Type string     `yaml:"type,omitempty"`
	OIDC OIDCConfig `yaml:"oidc,omitempty"`
}

// OIDCConfig is generic OIDC configuration for a profile. No provider-specific
// fields live here; a private preset overlay supplies concrete issuer/client
// values.
type OIDCConfig struct {
	Issuer   string   `yaml:"issuer,omitempty"`
	ClientID string   `yaml:"client_id,omitempty"`
	Scopes   []string `yaml:"scopes,omitempty"`
	Audience string   `yaml:"audience,omitempty"`
}

// ConfigDir returns the config directory for appName:
// $XDG_CONFIG_HOME/<app> when set, else ~/.config/<app>.
func ConfigDir(appName string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(base, appName)
}

// LoadConfig reads config.yaml from path (or the appName default when path is
// empty). A missing file yields an empty Config with no error.
func LoadConfig(appName, path string) (Config, error) {
	if path == "" {
		path = filepath.Join(ConfigDir(appName), "config.yaml")
	}
	var cfg Config
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// ResolveProfile selects the active profile and applies overrides in
// precedence order: flags (via the passed overrides) beat environment beat the
// config file. profileName selects which named profile; when empty it falls
// back to CurrentProfile, then to a lone profile, then to an empty default.
//
// Environment overrides (appName uppercased, non-alnum → underscore):
//   - <APP>_SERVER   → Server (base URL)
//   - <APP>_PROFILE  → profile selection (when profileName is empty)
func (c Config) ResolveProfile(appName, profileName, serverOverride string) (Profile, error) {
	env := envPrefix(appName)
	if profileName == "" {
		profileName = os.Getenv(env + "_PROFILE")
	}
	if profileName == "" {
		profileName = c.CurrentProfile
	}

	p := Profile{}
	switch {
	case profileName != "":
		var ok bool
		p, ok = c.Profiles[profileName]
		if !ok {
			return Profile{}, fmt.Errorf("profile %q not found in config", profileName)
		}
		p.Name = profileName
	case len(c.Profiles) == 1:
		for name, only := range c.Profiles {
			p, p.Name = only, name
		}
	}

	if v := os.Getenv(env + "_SERVER"); v != "" {
		p.Server = v
	}
	if serverOverride != "" {
		p.Server = serverOverride
	}
	return p, nil
}

// envPrefix maps an app name to its env-var prefix, e.g. "ib" → "IB",
// "my-cli" → "MY_CLI".
func envPrefix(appName string) string {
	var b strings.Builder
	for _, r := range appName {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
