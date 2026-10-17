package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mewebstudio/conductor-cli/internal/agents"
	"github.com/mewebstudio/conductor-cli/internal/paths"
)

// Targets resolves the selected agent keys that are installed on this machine.
// With no agents chosen yet it falls back to the detected ones.
func Targets(sel *Selection) []agents.Agent {
	keys := sel.Agents
	if len(keys) == 0 {
		keys = agents.Detected(sel.Custom)
	}
	var out []agents.Agent
	for _, k := range keys {
		if a, ok := agents.Find(k, sel.Custom); ok && a.Installed() {
			out = append(out, a)
		}
	}
	return out
}

// Combined returns synced plus machine-only skills.
func Combined() (*Selection, *Selection, error) {
	sel, err := Load()
	if err != nil {
		return nil, nil, err
	}
	loc, err := LoadLocal()
	if err != nil {
		return nil, nil, err
	}
	return sel, loc, nil
}

// Event reports one sync step.
type Event struct {
	Name   string
	Action string // installed, linked, removed, plugin-installed, plugin-removed, failed, skipped
	Detail string
}

// Pending reports whether anything in the selection is missing here.
func Pending() bool {
	sel, loc, err := Combined()
	if err != nil {
		return false
	}
	targets := Targets(sel)
	for _, k := range append(append([]Skill{}, sel.Skills...), loc.Skills...) {
		st := Inspect(k.Name, targets)
		if !st.Installed {
			return true
		}
		for _, l := range st.Links {
			if l == agents.Missing {
				return true
			}
		}
	}
	installed := InstalledPlugins()
	for _, p := range sel.Plugins {
		if !installed[p.ID] {
			return true
		}
	}
	return false
}

// Sync installs missing skills and plugins, links skills into every target
// agent, and removes what conductor installed but you no longer list.
func Sync(ctx context.Context, report func(Event)) error {
	sel, loc, err := Combined()
	if err != nil {
		return err
	}
	targets := Targets(sel)
	all := agents.All(sel.Custom)
	wanted := map[string]bool{}
	for _, k := range append(append([]Skill{}, sel.Skills...), loc.Skills...) {
		wanted[k.Name] = true
		st := Inspect(k.Name, targets)
		switch {
		case !st.Installed || (k.Ref != "" && st.Managed && st.Commit != k.Ref && !IsLocalSource(k.Source)):
			commit, problems, err := Install(ctx, k, targets, false)
			if err != nil {
				report(Event{k.Name, "failed", err.Error()})
				continue
			}
			report(Event{k.Name, "installed", short(commit)})
			reportLinks(k.Name, problems, report)
		default:
			missing := false
			for _, l := range st.Links {
				missing = missing || l == agents.Missing || l == agents.Copied
			}
			if missing {
				reportLinks(k.Name, LinkAll(k.Name, targets), report)
				report(Event{k.Name, "linked", ""})
			}
		}
	}
	for name := range LoadManifest() {
		if !wanted[name] {
			if err := Remove(name, all); err != nil {
				report(Event{name, "failed", err.Error()})
			} else {
				report(Event{name, "removed", ""})
			}
		}
	}

	ours := loadOwnedPlugins()
	for _, p := range sel.Plugins {
		if InstalledPlugins()[p.ID] {
			continue
		}
		if err := InstallPlugin(ctx, p); err != nil {
			report(Event{p.ID, "failed", err.Error()})
			continue
		}
		ours = append(ours, p.ID)
		report(Event{p.ID, "plugin-installed", ""})
	}
	var keep []string
	for _, id := range ours {
		if slices.ContainsFunc(sel.Plugins, func(p Plugin) bool { return p.ID == id }) {
			keep = append(keep, id)
			continue
		}
		if err := UninstallPlugin(ctx, id); err != nil {
			report(Event{id, "failed", err.Error()})
			keep = append(keep, id)
		} else {
			report(Event{id, "plugin-removed", ""})
		}
	}
	return saveOwnedPlugins(keep)
}

func reportLinks(name string, problems map[string]error, report func(Event)) {
	for agent, err := range problems {
		report(Event{name, "skipped", agent + ": " + err.Error()})
	}
}

func short(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

func ownedPluginsPath() string { return filepath.Join(paths.StateDir(), "plugins.json") }

func loadOwnedPlugins() []string {
	var ids []string
	if b, err := os.ReadFile(ownedPluginsPath()); err == nil {
		_ = json.Unmarshal(b, &ids)
	}
	return ids
}

func saveOwnedPlugins(ids []string) error {
	slices.Sort(ids)
	ids = slices.Compact(ids)
	b, _ := json.MarshalIndent(ids, "", "  ")
	if err := os.MkdirAll(paths.StateDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(ownedPluginsPath(), append(b, '\n'), 0o644)
}

// MarkPluginOwned records that conductor installed a plugin, so removing it
// from your list later uninstalls it.
func MarkPluginOwned(id string) error { return saveOwnedPlugins(append(loadOwnedPlugins(), id)) }

// Existing describes skills and plugins already on this machine that are not
// in your list yet, so they can be added in one step.
type Existing struct {
	Skills    []Skill  // with a known source
	Unknown   []string // in the shared folder, source unknown
	Plugins   []Plugin
	Untracked []string // in an agent folder only (not shared), left alone
}

// FindExisting scans the shared folder, Claude's skills folder, the
// vercel-labs `skills` lock file, Claude's marketplace clones and Claude's
// enabled plugins for things not yet in your list.
func FindExisting(sel *Selection) Existing {
	var ex Existing
	listed := map[string]bool{}
	for _, k := range sel.Skills {
		listed[k.Name] = true
	}
	lock := readSkillsLock()
	var market map[string]Skill
	resolve := func(name string) (Skill, bool) {
		if l, ok := lock[name]; ok {
			return l, true
		}
		if market == nil {
			market = marketplaceSkills()
		}
		l, ok := market[name]
		return l, ok
	}
	seen := map[string]bool{}
	claudeSkills := filepath.Join(paths.ClaudeDir(), "skills")
	for _, dir := range []string{agents.SharedDir(), claudeSkills} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			if listed[name] || seen[name] || name == "conductor" || strings.HasPrefix(name, ".") {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
				continue
			}
			seen[name] = true
			if k, ok := resolve(name); ok {
				ex.Skills = append(ex.Skills, k)
			} else if k, ok := fromGitClone(filepath.Join(dir, name), name); ok {
				ex.Skills = append(ex.Skills, k)
			} else if dir == agents.SharedDir() {
				ex.Unknown = append(ex.Unknown, name)
			} else {
				ex.Untracked = append(ex.Untracked, name)
			}
		}
	}
	slices.SortFunc(ex.Skills, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	have := map[string]bool{}
	for _, p := range sel.Plugins {
		have[p.ID] = true
	}
	for id := range InstalledPlugins() {
		if have[id] {
			continue
		}
		_, mk, _ := strings.Cut(id, "@")
		ex.Plugins = append(ex.Plugins, Plugin{ID: id, Marketplace: MarketplaceSource(mk)})
	}
	slices.SortFunc(ex.Plugins, func(a, b Plugin) int { return strings.Compare(a.ID, b.ID) })
	return ex
}

// fromGitClone uses the origin of a skill folder that is itself a git clone.
func fromGitClone(dir, name string) (Skill, bool) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return Skill{}, false
	}
	url, err := git(context.Background(), dir, "remote", "get-url", "origin")
	if err != nil || url == "" {
		return Skill{}, false
	}
	src := strings.TrimSuffix(url, ".git")
	if rest, ok := strings.CutPrefix(src, "https://github.com/"); ok {
		src = rest
	}
	return Skill{Name: name, Source: src}, true
}

// marketplaceSkills maps skill names found in Claude's local marketplace
// clones to their GitHub source.
func marketplaceSkills() map[string]Skill {
	out := map[string]Skill{}
	dirs, _ := filepath.Glob(filepath.Join(paths.ClaudeDir(), "plugins", "marketplaces", "*"))
	for _, d := range dirs {
		src := MarketplaceSource(filepath.Base(d))
		if src == "" || IsRemote(src) || IsLocalSource(src) {
			continue
		}
		found, _ := Discover(d)
		for _, f := range found {
			if _, dup := out[f.Name]; !dup {
				out[f.Name] = Skill{Name: f.Name, Source: src, Path: f.Path}
			}
		}
	}
	return out
}

// readSkillsLock maps skill names to sources from ~/.agents/.skill-lock.json.
func readSkillsLock() map[string]Skill {
	out := map[string]Skill{}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(agents.SharedDir()), ".skill-lock.json"))
	if err != nil {
		return out
	}
	var lock struct {
		Skills map[string]struct {
			Source     string `json:"source"`
			SourceType string `json:"sourceType"`
			SourceURL  string `json:"sourceUrl"`
			SkillPath  string `json:"skillPath"`
		} `json:"skills"`
	}
	if json.Unmarshal(b, &lock) != nil {
		return out
	}
	for name, l := range lock.Skills {
		if l.SourceType != "github" {
			continue // well-known/zip sources are not reproducible from git
		}
		path := strings.TrimSuffix(filepath.ToSlash(filepath.Dir(l.SkillPath)), ".")
		out[name] = Skill{Name: name, Source: l.Source, Path: strings.Trim(path, "/")}
	}
	return out
}
