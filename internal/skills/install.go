package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mewebstudio/conductor-cli/internal/agents"
	"github.com/mewebstudio/conductor-cli/internal/paths"
)

// Record is what conductor installed for a skill on this machine.
type Record struct {
	Source      string    `json:"source"`
	Path        string    `json:"path,omitempty"`
	Commit      string    `json:"commit,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
}

// Manifest maps skill names to install records.
type Manifest map[string]Record

func manifestPath() string { return filepath.Join(paths.StateDir(), "skills.json") }

// LoadManifest reads the install records.
func LoadManifest() Manifest {
	m := Manifest{}
	if b, err := os.ReadFile(manifestPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (m Manifest) save() error {
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(paths.StateDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(manifestPath(), append(b, '\n'), 0o644)
}

// SharedPath is the canonical folder of a skill.
func SharedPath(name string) string { return filepath.Join(agents.SharedDir(), name) }

// ErrForeign means a skill folder exists that conductor did not install.
var ErrForeign = errors.New("already installed by something else")

// Prepare fetches a skill's files and returns the folder holding SKILL.md
// and the exact commit (empty for local folders).
func Prepare(ctx context.Context, k Skill) (dir, commit string, err error) {
	if IsLocalSource(k.Source) {
		dir = k.Source
	} else {
		repo, c, err := Fetch(ctx, CloneURL(k.Source), k.Ref)
		if err != nil {
			return "", "", err
		}
		dir, commit = filepath.Join(repo, filepath.FromSlash(k.Path)), c
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		return "", "", fmt.Errorf("no SKILL.md in %s", dir)
	}
	return dir, commit, nil
}

// IsLocalSource reports whether a source is a folder on this machine.
func IsLocalSource(s string) bool { return filepath.IsAbs(s) }

// Install puts a skill into the shared folder (replacing conductor's own
// previous copy, never someone else's unless adopt is set) and links it into
// the given agents. It returns the installed commit and per-agent problems.
func Install(ctx context.Context, k Skill, targets []agents.Agent, adopt bool) (string, map[string]error, error) {
	if err := CheckName(k.Name); err != nil {
		return "", nil, err
	}
	src, commit, err := Prepare(ctx, k)
	if err != nil {
		return "", nil, err
	}
	man := LoadManifest()
	dst := SharedPath(k.Name)
	if _, err := os.Lstat(dst); err == nil {
		if _, managed := man[k.Name]; !managed && !adopt {
			return "", nil, fmt.Errorf("%s: %w (%s); use `conductor skills adopt %s`", k.Name, ErrForeign, dst, k.Name)
		}
	}
	tmp := dst + ".conductor-new"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(agents.SharedDir(), 0o755); err != nil {
		return "", nil, err
	}
	if err := agents.CopyDir(src, tmp); err != nil {
		return "", nil, err
	}
	if err := os.RemoveAll(dst); err != nil {
		return "", nil, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", nil, err
	}
	man[k.Name] = Record{Source: k.Source, Path: k.Path, Commit: commit, InstalledAt: time.Now().UTC()}
	if err := man.save(); err != nil {
		return "", nil, err
	}
	return commit, LinkAll(k.Name, targets), nil
}

// LinkAll links a shared skill into every non-universal target agent.
func LinkAll(name string, targets []agents.Agent) map[string]error {
	problems := map[string]error{}
	for _, a := range targets {
		if a.Universal() {
			continue
		}
		if err := agents.Link(a.SkillsDir(), name, SharedPath(name)); err != nil {
			problems[a.Key] = err
		}
	}
	return problems
}

// Adopt records an existing shared skill as managed without changing files.
func Adopt(k Skill, commit string) error {
	if _, err := os.Stat(filepath.Join(SharedPath(k.Name), "SKILL.md")); err != nil {
		return fmt.Errorf("%s is not in %s", k.Name, agents.SharedDir())
	}
	man := LoadManifest()
	man[k.Name] = Record{Source: k.Source, Path: k.Path, Commit: commit, InstalledAt: time.Now().UTC()}
	return man.save()
}

// Remove deletes conductor's links and shared copy of a skill.
func Remove(name string, all []agents.Agent) error {
	if err := CheckName(name); err != nil {
		return err
	}
	for _, a := range all {
		if !a.Universal() {
			_ = agents.Unlink(a.SkillsDir(), name, SharedPath(name))
		}
	}
	man := LoadManifest()
	if _, ok := man[name]; ok {
		if err := os.RemoveAll(SharedPath(name)); err != nil {
			return err
		}
		delete(man, name)
		return man.save()
	}
	return nil
}

// State summarizes a chosen skill on this machine.
type State struct {
	Installed bool
	Managed   bool
	Commit    string
	Links     map[string]agents.LinkState // agent key → state
}

// Inspect reports the state of a skill against the target agents.
func Inspect(name string, targets []agents.Agent) State {
	st := State{Links: map[string]agents.LinkState{}}
	if _, err := os.Stat(filepath.Join(SharedPath(name), "SKILL.md")); err == nil {
		st.Installed = true
	}
	if r, ok := LoadManifest()[name]; ok {
		st.Managed, st.Commit = true, r.Commit
	}
	for _, a := range targets {
		if a.Universal() {
			continue
		}
		st.Links[a.Key] = agents.Inspect(a.SkillsDir(), name, SharedPath(name))
	}
	return st
}
