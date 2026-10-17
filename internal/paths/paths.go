// Package paths resolves every location conductor reads or writes.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// Home returns the user's home directory, or "." if it cannot be resolved.
func Home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// Root is conductor's own directory (~/.conductor-cli). CONDUCTOR_HOME overrides it.
func Root() string {
	if v := os.Getenv("CONDUCTOR_HOME"); v != "" {
		return v
	}
	return filepath.Join(Home(), ".conductor-cli")
}

// ConfigDir holds the synced settings: by default ~/.conductor-cli/config (a clone
// of your settings repo), or any folder chosen with --config-path, which is
// remembered per machine. CONDUCTOR_CONFIG_DIR overrides both.
func ConfigDir() string {
	if v := os.Getenv("CONDUCTOR_CONFIG_DIR"); v != "" {
		return v
	}
	if b, err := os.ReadFile(locationFile()); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			return p
		}
	}
	return DefaultConfigDir()
}

// DefaultConfigDir is used when no custom location is set.
func DefaultConfigDir() string { return filepath.Join(Root(), "config") }

func locationFile() string { return filepath.Join(Root(), "config-location") }

// SetConfigDir remembers a custom settings folder on this machine; "" resets
// to the default.
func SetConfigDir(dir string) error {
	if dir == "" || filepath.Clean(dir) == filepath.Clean(DefaultConfigDir()) {
		err := os.Remove(locationFile())
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(Root(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(locationFile(), []byte(dir+"\n"), 0o644)
}

// Expand resolves ~ and makes a path absolute.
func Expand(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = filepath.Join(Home(), p[1:])
	}
	return filepath.Abs(p)
}

// CacheDir holds downloaded repositories.
func CacheDir() string { return filepath.Join(Root(), "cache") }

// SkillsFile lists your chosen skills and plugins (synced).
func SkillsFile() string { return filepath.Join(ConfigDir(), "skills.toml") }

// LocalSkillsFile lists skills from local folders (this machine only).
func LocalSkillsFile() string { return filepath.Join(Root(), "skills.local.toml") }

// ConfigFile is the synced settings file.
func ConfigFile() string { return filepath.Join(ConfigDir(), "conductor.toml") }

// LocalFile holds machine-only overrides; it is never synced.
func LocalFile() string { return filepath.Join(Root(), "local.toml") }

// StateDir holds machine state such as usage counters and the managed-file manifest.
func StateDir() string { return filepath.Join(Root(), "state") }

// BackupsDir holds copies of files taken before conductor overwrites them.
func BackupsDir() string { return filepath.Join(Root(), "backups") }

// ProvidersDir holds user-installed external providers.
func ProvidersDir() string { return filepath.Join(Root(), "providers") }

// ClaudeDir is Claude Code's user directory. CLAUDE_CONFIG_DIR overrides it.
func ClaudeDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	return filepath.Join(Home(), ".claude")
}

// EnsureDirs creates conductor's machine directories.
func EnsureDirs() error {
	for _, d := range []string{Root(), StateDir(), BackupsDir(), ProvidersDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
