// Package config loads and edits conductor's layered settings:
// built-in defaults < synced conductor.toml < machine-only local.toml.
package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/mewebstudio/conductor-cli/internal/paths"
)

//go:embed defaults.toml
var defaultsTOML string

// Role is one Claude model slot (lead, workers, reviewer agent).
type Role struct {
	Enabled bool   `toml:"enabled"`
	Model   string `toml:"model"`
	Effort  string `toml:"effort"`
}

// Reviewer holds per-provider review settings.
type Reviewer struct {
	Model      string `toml:"model"`
	Effort     string `toml:"effort"`
	DailyLimit int    `toml:"daily_limit"`
}

// Config is the merged, effective configuration.
type Config struct {
	Profile string `toml:"profile"`
	Models  struct {
		Lead     Role `toml:"lead"`
		Mini     Role `toml:"mini"`
		Standard Role `toml:"standard"`
		Expert   Role `toml:"expert"`
		Reviewer Role `toml:"reviewer"`
	} `toml:"models"`
	Review struct {
		Chain     []string `toml:"chain"`
		Policy    string   `toml:"policy"`
		FixRounds int      `toml:"fix_rounds"`
	} `toml:"review"`
	Reviewers map[string]Reviewer `toml:"reviewers"`
	Providers struct {
		Enabled []string `toml:"enabled"`
	} `toml:"providers"`
	Git struct {
		Commit         string `toml:"commit"`
		CommitLanguage string `toml:"commit_language"`
		Push           string `toml:"push"`
		AutoBranch     bool   `toml:"auto_branch"`
		BranchPrefix   string `toml:"branch_prefix"`
	} `toml:"git"`
	Parallel struct {
		MaxWorkers int  `toml:"max_workers"`
		Worktrees  bool `toml:"worktrees"`
	} `toml:"parallel"`
	Lead struct {
		Coding      string `toml:"coding"`
		ReportWords int    `toml:"report_words"`
		Escalations int    `toml:"escalations"`
	} `toml:"lead"`
	Comms struct {
		Language string `toml:"language"`
		Address  string `toml:"address"`
		Notify   string `toml:"notify"`
	} `toml:"comms"`
	Docs struct {
		Decisions string `toml:"decisions"`
		Handoff   string `toml:"handoff"`
		Reviews   string `toml:"reviews"`
	} `toml:"docs"`
	Obsidian struct {
		Enabled bool   `toml:"enabled"`
		Vault   string `toml:"vault"`
	} `toml:"obsidian"`
	Sync struct {
		Auto bool `toml:"auto"`
	} `toml:"sync"`
}

// ProviderEnabled reports whether an optional provider is switched on.
func (c *Config) ProviderEnabled(name string) bool {
	for _, p := range c.Providers.Enabled {
		if p == name {
			return true
		}
	}
	return false
}

// Reviewer returns the settings for a review provider (zero value if absent).
func (c *Config) Reviewer(name string) Reviewer { return c.Reviewers[name] }

// Defaults returns the built-in defaults as a raw tree.
func Defaults() map[string]any {
	m := map[string]any{}
	if _, err := toml.Decode(defaultsTOML, &m); err != nil {
		panic(fmt.Sprintf("conductor: invalid embedded defaults: %v", err))
	}
	return m
}

// ReadTree reads a TOML file as a raw tree; a missing file yields an empty tree.
func ReadTree(path string) (map[string]any, error) {
	m := map[string]any{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(b), &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// WriteTree writes a raw tree as TOML, creating parent directories.
func WriteTree(path string, m map[string]any) error {
	var buf bytes.Buffer
	buf.WriteString("# Managed by conductor. Edit with `conductor config` or by hand.\n\n")
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// Merge deep-merges src into dst; tables merge key by key, everything else replaces.
func Merge(dst, src map[string]any) map[string]any {
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				dst[k] = Merge(dv, sv)
				continue
			}
		}
		dst[k] = v
	}
	return dst
}

// Load returns the effective configuration.
func Load() (*Config, error) {
	tree := Defaults()
	for _, p := range []string{paths.ConfigFile(), paths.LocalFile()} {
		t, err := ReadTree(p)
		if err != nil {
			return nil, err
		}
		Merge(tree, t)
	}
	return decode(tree)
}

func decode(tree map[string]any) (*Config, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(tree); err != nil {
		return nil, err
	}
	c := &Config{}
	if _, err := toml.Decode(buf.String(), c); err != nil {
		return nil, err
	}
	if c.Reviewers == nil {
		c.Reviewers = map[string]Reviewer{}
	}
	return c, nil
}

// Target picks which file an edit goes to.
func Target(local bool) string {
	if local {
		return paths.LocalFile()
	}
	return paths.ConfigFile()
}

// Lookup returns the value at a dotted key in a tree.
func Lookup(tree map[string]any, key string) (any, bool) {
	cur := any(tree)
	for _, part := range strings.Split(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// SetValue stores an already-typed value at a dotted key in the chosen file.
func SetValue(key string, value any, local bool) error {
	path := Target(local)
	tree, err := ReadTree(path)
	if err != nil {
		return err
	}
	parts := strings.Split(key, ".")
	cur := tree
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
	return WriteTree(path, tree)
}

// UnsetValue removes a dotted key from the chosen file (used to drop local overrides).
func UnsetValue(key string, local bool) error {
	path := Target(local)
	tree, err := ReadTree(path)
	if err != nil {
		return err
	}
	parts := strings.Split(key, ".")
	cur := tree
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	delete(cur, parts[len(parts)-1])
	return WriteTree(path, tree)
}

// choices lists the allowed values of enumerated settings.
var choices = map[string][]string{
	"git.commit":          {"ask", "milestones", "every-step", "manual"},
	"git.push":            {"never", "ask", "auto"},
	"git.commit_language": {"en", "tr"},
	"lead.coding":         {"small-edits", "strict"},
	"comms.language":      {"tr", "en"},
	"comms.notify":        {"off", "decisions", "all"},
	"review.policy":       {"milestones", "manual", "every-commit"},
}

// ranges bounds numeric settings (inclusive).
var ranges = map[string][2]int64{
	"parallel.max_workers": {1, 8},
	"lead.escalations":     {0, 2},
	"lead.report_words":    {50, 2000},
	"review.fix_rounds":    {0, 3},
}

// SetString parses raw text into the type the key has in the effective tree
// (bool, int, string list or string), validates it and stores it.
func SetString(key, raw string, local bool) error {
	if allowed, ok := choices[key]; ok && !slices.Contains(allowed, raw) {
		return fmt.Errorf("%s must be one of %v", key, allowed)
	}
	if key == "profile" {
		return fmt.Errorf("use `conductor profile <name>` to switch profiles")
	}
	eff, err := EffectiveTree()
	if err != nil {
		return err
	}
	cur, ok := Lookup(eff, key)
	if !ok {
		if !strings.HasPrefix(key, "reviewers.") {
			return fmt.Errorf("unknown setting %q (see `conductor config`)", key)
		}
		return SetValue(key, raw, local)
	}
	var v any
	switch cur.(type) {
	case bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("%s expects true or false", key)
		}
		v = b
	case int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("%s expects a whole number", key)
		}
		if r, ok := ranges[key]; ok && (n < r[0] || n > r[1]) {
			return fmt.Errorf("%s must be between %d and %d", key, r[0], r[1])
		}
		v = n
	case []any:
		v = SplitList(raw)
	case map[string]any:
		return fmt.Errorf("%s is a section; set one of its keys instead", key)
	default:
		v = raw
	}
	return SetValue(key, v, local)
}

// SplitList turns "a, b c" into []string{"a","b","c"}.
func SplitList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' })
}

// EffectiveTree returns the merged raw tree.
func EffectiveTree() (map[string]any, error) {
	tree := Defaults()
	for _, p := range []string{paths.ConfigFile(), paths.LocalFile()} {
		t, err := ReadTree(p)
		if err != nil {
			return nil, err
		}
		Merge(tree, t)
	}
	return tree, nil
}

// Flatten lists every leaf as "dotted.key = value", sorted.
func Flatten(tree map[string]any) []string {
	var out []string
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			if sub, ok := v.(map[string]any); ok {
				walk(key, sub)
				continue
			}
			out = append(out, fmt.Sprintf("%s = %s", key, format(v)))
		}
	}
	walk("", tree)
	sort.Strings(out)
	return out
}

func format(v any) string {
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = format(x)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []string:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = strconv.Quote(x)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprint(t)
	}
}

// Exists reports whether the synced settings file has been created.
func Exists() bool {
	_, err := os.Stat(paths.ConfigFile())
	return err == nil
}
