// Package agents knows where coding agents look for user-level skills, so one
// copy in ~/.agents/skills can be linked into every agent you use.
package agents

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mewebstudio/conductor-cli/internal/paths"
)

// Loc is a filesystem location: an absolute path, or a path relative to an
// environment-overridable base under the home directory.
type Loc struct {
	Abs     string
	Env     string // env var that replaces the base, e.g. CLAUDE_CONFIG_DIR
	Default string // base relative to home when Env is unset, e.g. ".claude"
	Rel     string
}

// P builds a home-relative location.
func P(env, def, rel string) Loc { return Loc{Env: env, Default: def, Rel: rel} }

// A builds an absolute location.
func A(abs string) Loc { return Loc{Abs: abs} }

// Path resolves the location for this machine.
func (l Loc) Path() string {
	if l.Abs != "" {
		return l.Abs
	}
	base := filepath.Join(paths.Home(), filepath.FromSlash(l.Default))
	if l.Env != "" {
		if v := strings.TrimSpace(os.Getenv(l.Env)); v != "" {
			base = v
		}
	}
	return filepath.Join(base, filepath.FromSlash(l.Rel))
}

// Agent is a coding agent with a global skills folder.
type Agent struct {
	Key    string
	Name   string
	Dir    Loc
	Detect []Loc
}

// SkillsDir is where this agent reads user-level skills.
func (a Agent) SkillsDir() string { return a.Dir.Path() }

// Installed reports whether the agent appears to be installed here.
func (a Agent) Installed() bool {
	for _, d := range a.Detect {
		if _, err := os.Stat(d.Path()); err == nil {
			return true
		}
	}
	return false
}

// Universal reports whether the agent reads the shared ~/.agents/skills folder
// directly, so no link is needed.
func (a Agent) Universal() bool { return filepath.Clean(a.SkillsDir()) == filepath.Clean(SharedDir()) }

// SharedDir is the canonical skills folder every install lands in first.
func SharedDir() string { return filepath.Join(paths.Home(), ".agents", "skills") }

// Custom adds an agent from user settings (key → skills folder).
func Custom(key, dir string) Agent {
	if strings.HasPrefix(dir, "~") {
		dir = filepath.Join(paths.Home(), strings.TrimPrefix(strings.TrimPrefix(dir, "~"), string(filepath.Separator)))
	}
	return Agent{Key: key, Name: key, Dir: A(dir), Detect: []Loc{A(filepath.Dir(dir))}}
}

// All returns built-in agents plus custom ones, sorted by name.
func All(custom map[string]string) []Agent {
	out := append([]Agent{}, table...)
	for k, d := range custom {
		out = append(out, Custom(k, d))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Find returns an agent by key.
func Find(key string, custom map[string]string) (Agent, bool) {
	for _, a := range All(custom) {
		if a.Key == key {
			return a, true
		}
	}
	return Agent{}, false
}

// Detected returns the keys of agents installed on this machine.
func Detected(custom map[string]string) []string {
	var keys []string
	for _, a := range All(custom) {
		if a.Installed() {
			keys = append(keys, a.Key)
		}
	}
	return keys
}
