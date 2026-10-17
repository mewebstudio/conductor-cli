// Package skills installs the skills and Claude plugins you choose, once into
// ~/.agents/skills and linked into every coding agent you use.
package skills

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/BurntSushi/toml"

	"github.com/mewebstudio/conductor-cli/internal/paths"
)

// Skill is one chosen skill.
type Skill struct {
	Name   string `toml:"name"`
	Source string `toml:"source"`         // owner/repo, git URL, or a local folder
	Path   string `toml:"path,omitempty"` // folder inside the repo that holds SKILL.md
	Ref    string `toml:"ref,omitempty"`  // pinned commit
}

// Plugin is one chosen Claude Code plugin.
type Plugin struct {
	ID          string `toml:"id"`                    // name@marketplace
	Marketplace string `toml:"marketplace,omitempty"` // marketplace source (owner/repo or URL) to add if missing
}

// Selection is the synced list of your agents, skills and plugins.
type Selection struct {
	Agents  []string          `toml:"agents,omitempty"`
	Custom  map[string]string `toml:"custom_agents,omitempty"` // key → skills folder
	Skills  []Skill           `toml:"skill,omitempty"`
	Plugins []Plugin          `toml:"plugin,omitempty"`
}

func read(path string) (*Selection, error) {
	s := &Selection{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	_, err = toml.Decode(string(b), s)
	return s, err
}

func write(path string, s *Selection) error {
	sort.Slice(s.Skills, func(i, j int) bool { return s.Skills[i].Name < s.Skills[j].Name })
	sort.Slice(s.Plugins, func(i, j int) bool { return s.Plugins[i].ID < s.Plugins[j].ID })
	var buf bytes.Buffer
	buf.WriteString("# Your skills and plugins. Managed by `conductor skills`.\n\n")
	if err := toml.NewEncoder(&buf).Encode(s); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// Load returns the synced selection.
func Load() (*Selection, error) { return read(paths.SkillsFile()) }

// LoadLocal returns skills from local folders (this machine only).
func LoadLocal() (*Selection, error) { return read(paths.LocalSkillsFile()) }

// Save writes the synced selection.
func (s *Selection) Save() error { return write(paths.SkillsFile(), s) }

// SaveLocal writes the machine-only selection.
func (s *Selection) SaveLocal() error { return write(paths.LocalSkillsFile(), s) }

// Find returns a skill by name.
func (s *Selection) Find(name string) (Skill, bool) {
	i := slices.IndexFunc(s.Skills, func(k Skill) bool { return k.Name == name })
	if i < 0 {
		return Skill{}, false
	}
	return s.Skills[i], true
}

// Put adds or replaces a skill.
func (s *Selection) Put(k Skill) {
	if i := slices.IndexFunc(s.Skills, func(x Skill) bool { return x.Name == k.Name }); i >= 0 {
		s.Skills[i] = k
		return
	}
	s.Skills = append(s.Skills, k)
}

// Drop removes a skill or plugin by name/id and reports whether it existed.
func (s *Selection) Drop(name string) bool {
	n := len(s.Skills) + len(s.Plugins)
	s.Skills = slices.DeleteFunc(s.Skills, func(k Skill) bool { return k.Name == name })
	s.Plugins = slices.DeleteFunc(s.Plugins, func(p Plugin) bool { return p.ID == name || pluginName(p.ID) == name })
	return len(s.Skills)+len(s.Plugins) != n
}

// PutPlugin adds a plugin if missing.
func (s *Selection) PutPlugin(p Plugin) {
	if !slices.ContainsFunc(s.Plugins, func(x Plugin) bool { return x.ID == p.ID }) {
		s.Plugins = append(s.Plugins, p)
	}
}
