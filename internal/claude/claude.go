// Package claude renders conductor's skill, agents and settings into Claude Code's
// user directory, touching only what conductor owns.
package claude

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/mewebstudio/conductor-cli/internal/catalog"
	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/fsutil"
	"github.com/mewebstudio/conductor-cli/internal/ojson"
	"github.com/mewebstudio/conductor-cli/internal/paths"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"label": catalog.ModelLabel,
	"chain": func(c []string) string { return strings.Join(c, " → ") },
	"who": func(c *config.Config) string {
		if c.Comms.Address != "" {
			return c.Comms.Address
		}
		return "the user"
	},
}).ParseFS(templateFS, "templates/*.tmpl"))

const (
	blockBegin = "<!-- conductor:begin -->"
	blockEnd   = "<!-- conductor:end -->"
)

// File is one rendered, fully conductor-owned file.
type File struct {
	Path string
	Data []byte
}

func render(name string, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SkillPath is where the conductor skill is installed.
func SkillPath() string { return filepath.Join(paths.ClaudeDir(), "skills", "conductor", "SKILL.md") }

// AgentPath is where an agent definition is installed.
func AgentPath(name string) string { return filepath.Join(paths.ClaudeDir(), "agents", name+".md") }

// SettingsPath is Claude Code's user settings file.
func SettingsPath() string { return filepath.Join(paths.ClaudeDir(), "settings.json") }

// MemoryPath is Claude Code's user-level CLAUDE.md.
func MemoryPath() string { return filepath.Join(paths.ClaudeDir(), "CLAUDE.md") }

// Render produces every conductor-owned file for the configuration.
func Render(c *config.Config) ([]File, error) {
	skill, err := render("SKILL.md.tmpl", map[string]any{"C": c})
	if err != nil {
		return nil, err
	}
	files := []File{{SkillPath(), skill}}
	workers := []struct {
		key, tier string
		role      config.Role
	}{
		{"mini", "mini", c.Models.Mini},
		{"standard", "standard", c.Models.Standard},
		{"expert", "expert", c.Models.Expert},
	}
	for _, w := range workers {
		if !w.role.Enabled {
			continue
		}
		r, _ := catalog.FindRole(w.key)
		data, err := render("worker.md.tmpl", map[string]any{
			"C": c, "Name": r.Agent, "Tier": w.tier, "Role": w.role, "Summary": r.Summary,
		})
		if err != nil {
			return nil, err
		}
		files = append(files, File{AgentPath(r.Agent), data})
	}
	if c.Models.Reviewer.Enabled {
		data, err := render("reviewer.md.tmpl", map[string]any{"C": c, "Role": c.Models.Reviewer})
		if err != nil {
			return nil, err
		}
		files = append(files, File{AgentPath("reviewer"), data})
	}
	return files, nil
}

// Change describes one file conductor touched or would touch.
type Change struct {
	Path   string
	Action string // created, updated, removed, unchanged, edited-by-hand
}

// Apply writes rendered files, removes ones no longer wanted, and merges
// settings.json and CLAUDE.md. hookCmd is the conductor binary used for the
// session-start sync hook ("" leaves hooks alone).
func Apply(c *config.Config, hookCmd string) ([]Change, error) {
	files, err := Render(c)
	if err != nil {
		return nil, err
	}
	man := fsutil.LoadManifest()
	var changes []Change
	wanted := map[string]bool{}
	for _, f := range files {
		wanted[f.Path] = true
		res, err := fsutil.WriteIfChanged(f.Path, f.Data, 0o644)
		if err != nil {
			return changes, err
		}
		man[f.Path] = fsutil.Hash(f.Data)
		changes = append(changes, Change{f.Path, res.String()})
	}
	for _, p := range man.Paths() {
		if wanted[p] || !isOwnedFile(p) {
			continue
		}
		if _, err := fsutil.Backup(p); err != nil {
			return changes, err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return changes, err
		}
		delete(man, p)
		changes = append(changes, Change{p, "removed"})
	}
	if err := man.Save(); err != nil {
		return changes, err
	}
	ch, err := applySettings(c, hookCmd)
	if err != nil {
		return changes, err
	}
	changes = append(changes, ch)
	ch, err = applyMemory(c)
	if err != nil {
		return changes, err
	}
	return append(changes, ch), nil
}

// isOwnedFile limits removals to files conductor renders itself.
func isOwnedFile(p string) bool {
	return p == SkillPath() || strings.HasPrefix(p, filepath.Join(paths.ClaudeDir(), "agents")+string(filepath.Separator))
}

// Status compares what is on disk with what the configuration renders.
func Status(c *config.Config) ([]Change, error) {
	files, err := Render(c)
	if err != nil {
		return nil, err
	}
	man := fsutil.LoadManifest()
	var out []Change
	for _, f := range files {
		cur, err := os.ReadFile(f.Path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			out = append(out, Change{f.Path, "missing"})
		case err != nil:
			return nil, err
		case bytes.Equal(cur, f.Data):
			out = append(out, Change{f.Path, "ok"})
		case man[f.Path] != "" && man[f.Path] != fsutil.Hash(cur):
			out = append(out, Change{f.Path, "edited-by-hand"})
		default:
			out = append(out, Change{f.Path, "outdated"})
		}
	}
	return out, nil
}

func readSettings() (*ojson.Object, []byte, error) {
	raw, err := os.ReadFile(SettingsPath())
	if errors.Is(err, os.ErrNotExist) {
		return &ojson.Object{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	obj, err := ojson.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", SettingsPath(), err)
	}
	return obj, raw, nil
}

func applySettings(c *config.Config, hookCmd string) (Change, error) {
	obj, _, err := readSettings()
	if err != nil {
		return Change{}, err
	}
	obj.Set("model", c.Models.Lead.Model)
	if c.Models.Lead.Effort == "" {
		obj.Delete("effortLevel")
	} else {
		obj.Set("effortLevel", c.Models.Lead.Effort)
	}
	if hookCmd != "" {
		setSyncHook(obj, hookCmd, c.Sync.Auto)
	}
	setPermissions(obj, true)
	data, err := ojson.Marshal(obj)
	if err != nil {
		return Change{}, err
	}
	res, err := fsutil.WriteIfChanged(SettingsPath(), data, 0o644)
	return Change{SettingsPath(), res.String()}, err
}

// allowRules let the lead run conductor's own read-only helpers without a
// permission prompt; nothing else is allowed.
var allowRules = []string{
	"Bash(conductor review run *)",
	"Bash(conductor ask *)",
	"Bash(conductor notify *)",
	"Bash(echo $CLAUDE_EFFORT)",
}

// setPermissions adds (or removes) conductor's rules in permissions.allow,
// leaving every other rule untouched.
func setPermissions(settings *ojson.Object, add bool) {
	pv, _ := settings.Get("permissions")
	perms, ok := pv.(*ojson.Object)
	if !ok {
		if !add {
			return
		}
		perms = &ojson.Object{}
		settings.Set("permissions", perms)
	}
	av, _ := perms.Get("allow")
	list, _ := av.([]any)
	ours := map[string]bool{}
	for _, r := range allowRules {
		ours[r] = true
	}
	var kept []any
	have := map[string]bool{}
	for _, v := range list {
		r, _ := v.(string)
		if ours[r] && !add {
			continue
		}
		have[r] = true
		kept = append(kept, v)
	}
	if add {
		for _, r := range allowRules {
			if !have[r] {
				kept = append(kept, r)
			}
		}
	}
	if len(kept) == 0 {
		perms.Delete("allow")
	} else {
		perms.Set("allow", kept)
	}
	if len(perms.Members) == 0 {
		settings.Delete("permissions")
	}
}

// isSyncHook recognises the hook entry conductor installs.
func isSyncHook(v any) bool {
	h, ok := v.(*ojson.Object)
	if !ok {
		return false
	}
	cmd, _ := h.Get("command")
	s, _ := cmd.(string)
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(s)), ".exe")
	if base != "conductor" {
		return false
	}
	args, _ := h.Get("args")
	a, _ := args.([]any)
	return len(a) > 0 && a[0] == "sync"
}

// setSyncHook adds or removes the SessionStart "conductor sync --hook" entry,
// leaving every other hook untouched.
func setSyncHook(settings *ojson.Object, hookCmd string, enabled bool) {
	hv, _ := settings.Get("hooks")
	hooks, ok := hv.(*ojson.Object)
	if !ok {
		if !enabled {
			return
		}
		hooks = &ojson.Object{}
		settings.Set("hooks", hooks)
	}
	sv, _ := hooks.Get("SessionStart")
	groups, _ := sv.([]any)
	var kept []any
	for _, g := range groups {
		grp, ok := g.(*ojson.Object)
		if !ok {
			kept = append(kept, g)
			continue
		}
		hl, _ := grp.Get("hooks")
		list, _ := hl.([]any)
		var rest []any
		for _, h := range list {
			if !isSyncHook(h) {
				rest = append(rest, h)
			}
		}
		if len(rest) == 0 && len(list) > 0 {
			continue
		}
		grp.Set("hooks", rest)
		kept = append(kept, grp)
	}
	if enabled {
		entry := &ojson.Object{Members: []ojson.Member{
			{Key: "type", Value: "command"},
			{Key: "command", Value: hookCmd},
			{Key: "args", Value: []any{"sync", "--hook"}},
			{Key: "timeout", Value: json.Number("20")},
		}}
		kept = append(kept, &ojson.Object{Members: []ojson.Member{
			{Key: "matcher", Value: "startup"},
			{Key: "hooks", Value: []any{entry}},
		}})
	}
	if len(kept) == 0 {
		hooks.Delete("SessionStart")
	} else {
		hooks.Set("SessionStart", kept)
	}
	if len(hooks.Members) == 0 {
		settings.Delete("hooks")
	}
}

// SettingsState reports the lead model/effort and hook presence in settings.json.
func SettingsState() (model, effort string, hook bool, err error) {
	obj, _, err := readSettings()
	if err != nil {
		return "", "", false, err
	}
	if v, ok := obj.Get("model"); ok {
		model, _ = v.(string)
	}
	if v, ok := obj.Get("effortLevel"); ok {
		effort, _ = v.(string)
	}
	if hv, ok := obj.Get("hooks"); ok {
		if hooks, ok := hv.(*ojson.Object); ok {
			sv, _ := hooks.Get("SessionStart")
			groups, _ := sv.([]any)
			for _, g := range groups {
				if grp, ok := g.(*ojson.Object); ok {
					hl, _ := grp.Get("hooks")
					list, _ := hl.([]any)
					for _, h := range list {
						hook = hook || isSyncHook(h)
					}
				}
			}
		}
	}
	return model, effort, hook, nil
}

func applyMemory(c *config.Config) (Change, error) {
	block, err := render("claude-md.tmpl", map[string]any{"C": c})
	if err != nil {
		return Change{}, err
	}
	cur, err := os.ReadFile(MemoryPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Change{}, err
	}
	next := replaceBlock(string(cur), strings.TrimRight(string(block), "\n"))
	res, err := fsutil.WriteIfChanged(MemoryPath(), []byte(next), 0o644)
	return Change{MemoryPath(), res.String()}, err
}

// replaceBlock swaps the managed block in doc, or appends it; body "" removes it.
func replaceBlock(doc, body string) string {
	start := strings.Index(doc, blockBegin)
	end := strings.Index(doc, blockEnd)
	var before, after string
	if start >= 0 && end > start {
		before = doc[:start]
		after = doc[end+len(blockEnd):]
	} else {
		before = doc
	}
	before = strings.TrimRight(before, "\n")
	after = strings.TrimLeft(after, "\n")
	var parts []string
	if before != "" {
		parts = append(parts, before)
	}
	if body != "" {
		parts = append(parts, blockBegin+"\n"+body+"\n"+blockEnd)
	}
	if strings.TrimSpace(after) != "" {
		parts = append(parts, strings.TrimRight(after, "\n"))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// Uninstall removes everything conductor wrote into Claude's directory. It
// leaves the lead model setting in place unless resetModel is true.
func Uninstall(resetModel bool) ([]Change, error) {
	man := fsutil.LoadManifest()
	var out []Change
	for _, p := range man.Paths() {
		if !isOwnedFile(p) {
			continue
		}
		if _, err := fsutil.Backup(p); err != nil {
			return out, err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
		delete(man, p)
		out = append(out, Change{p, "removed"})
	}
	_ = os.Remove(filepath.Dir(SkillPath()))
	if err := man.Save(); err != nil {
		return out, err
	}
	obj, raw, err := readSettings()
	if err != nil {
		return out, err
	}
	if raw != nil {
		setSyncHook(obj, "", false)
		setPermissions(obj, false)
		if resetModel {
			obj.Delete("model")
			obj.Delete("effortLevel")
		}
		data, err := ojson.Marshal(obj)
		if err != nil {
			return out, err
		}
		res, err := fsutil.WriteIfChanged(SettingsPath(), data, 0o644)
		if err != nil {
			return out, err
		}
		out = append(out, Change{SettingsPath(), res.String()})
	}
	if cur, err := os.ReadFile(MemoryPath()); err == nil {
		next := replaceBlock(string(cur), "")
		if next == "" {
			if _, err := fsutil.Backup(MemoryPath()); err != nil {
				return out, err
			}
			_ = os.Remove(MemoryPath())
			out = append(out, Change{MemoryPath(), "removed"})
		} else {
			res, err := fsutil.WriteIfChanged(MemoryPath(), []byte(next), 0o644)
			if err != nil {
				return out, err
			}
			out = append(out, Change{MemoryPath(), res.String()})
		}
	}
	return out, nil
}
