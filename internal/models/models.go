// Package models discovers which models each tool offers, from the tools'
// own caches and commands, so nobody has to type model ids by hand.
//
// Sources (all best effort, with a built-in fallback):
//   - Claude: ~/.claude/cache/model-catalog/*.json (Claude Code's catalog for your account)
//   - Codex:  ~/.codex/models_cache.json (Codex CLI's catalog)
//   - Antigravity: `agy models`
//   - NVIDIA: the /v1/models API (see providers)
package models

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mewebstudio/conductor-cli/internal/paths"
)

// Model is one selectable model.
type Model struct {
	ID      string
	Label   string
	Efforts []string // supported effort levels; empty = no effort control
	Current bool     // shown in the tool's main list (false = older/overflow)
}

// Supports reports whether effort is valid for the model ("" always is).
func (m Model) Supports(effort string) bool {
	return effort == "" || slices.Contains(m.Efforts, effort)
}

// Find returns a model by id.
func Find(list []Model, id string) (Model, bool) {
	i := slices.IndexFunc(list, func(m Model) bool { return m.ID == id })
	if i < 0 {
		return Model{}, false
	}
	return list[i], true
}

// IDs lists model ids.
func IDs(list []Model) []string {
	out := make([]string, len(list))
	for i, m := range list {
		out[i] = m.ID
	}
	return out
}

var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// claudeFallback is used when Claude Code's catalog cannot be read.
var claudeFallback = []Model{
	{"claude-opus-5-5", "Opus 5.5", claudeEfforts, true},
	{"claude-sonnet-5-5", "Sonnet 5.5", claudeEfforts, true},
	{"claude-haiku-5-5", "Haiku 5.5", claudeEfforts, true},
	{"claude-fable-5-1", "Fable 5.1", claudeEfforts, true},
	{"claude-opus-5", "Opus 5", claudeEfforts, false},
	{"claude-sonnet-5", "Sonnet 5", claudeEfforts, false},
	{"claude-haiku-4-5", "Haiku 4.5", nil, false},
}

// Claude returns the models Claude Code offers this account, newest catalog
// first; fromCache is false when the built-in fallback was used.
func Claude() (list []Model, fromCache bool) {
	files, _ := filepath.Glob(filepath.Join(paths.ClaudeDir(), "cache", "model-catalog", "*.json"))
	var best struct {
		at     int64
		models []Model
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc struct {
			FetchedAt int64 `json:"fetchedAt"`
			Catalog   struct {
				Config struct {
					Models []struct {
						ID       string `json:"id"`
						Name     string `json:"name"`
						Section  string `json:"section"`
						Thinking struct {
							EffortOptions []struct {
								ID string `json:"id"`
							} `json:"effort_options"`
						} `json:"thinking"`
						Runtime struct {
							EffortLevels []string `json:"effort_levels"`
						} `json:"runtime"`
					} `json:"models"`
				} `json:"config"`
			} `json:"catalog"`
		}
		if json.Unmarshal(b, &doc) != nil || doc.FetchedAt <= best.at || len(doc.Catalog.Config.Models) == 0 {
			continue
		}
		var ms []Model
		for _, m := range doc.Catalog.Config.Models {
			eff := m.Runtime.EffortLevels
			if len(eff) == 0 {
				for _, o := range m.Thinking.EffortOptions {
					eff = append(eff, o.ID)
				}
			}
			label := m.Name
			if label == "" {
				label = m.ID
			}
			ms = append(ms, Model{ID: m.ID, Label: label, Efforts: eff, Current: m.Section != "overflow"})
		}
		best.at, best.models = doc.FetchedAt, ms
	}
	if len(best.models) == 0 {
		return claudeFallback, false
	}
	return withAliases(best.models), true
}

// withAliases adds the undated id for dated entries (claude-haiku-4-5-20251001
// is also accepted as claude-haiku-4-5).
func withAliases(list []Model) []Model {
	out := list
	for _, m := range list {
		parts := strings.Split(m.ID, "-")
		if last := parts[len(parts)-1]; len(last) == 8 && strings.Trim(last, "0123456789") == "" {
			alias := strings.Join(parts[:len(parts)-1], "-")
			if _, ok := Find(out, alias); !ok {
				a := m
				a.ID = alias
				out = append(out, a)
			}
		}
	}
	return out
}

var codexFallback = []Model{
	{"gpt-6.1-sol", "GPT-6.1-Sol", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, true},
}

// Codex returns the models the Codex CLI lists for your account.
func Codex() (list []Model, fromCache bool) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		home = filepath.Join(paths.Home(), ".codex")
	}
	b, err := os.ReadFile(filepath.Join(home, "models_cache.json"))
	if err != nil {
		return codexFallback, false
	}
	var doc struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &doc) != nil || len(doc.Models) == 0 {
		return codexFallback, false
	}
	for _, m := range doc.Models {
		if m.Visibility == "hide" {
			continue
		}
		var eff []string
		for _, l := range m.Levels {
			eff = append(eff, l.Effort)
		}
		list = append(list, Model{ID: m.Slug, Label: m.DisplayName, Efforts: eff, Current: true})
	}
	return list, true
}

// Antigravity runs `agy models`, which prints "id<TAB>label" lines; ids such
// as gemini-4-argon-high already carry their effort level.
func Antigravity(ctx context.Context) ([]Model, error) {
	bin, err := exec.LookPath("agy")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "models").Output()
	if err != nil {
		return nil, err
	}
	var list []Model
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		id, label, _ := strings.Cut(sc.Text(), "\t")
		id, label = strings.TrimSpace(id), strings.TrimSpace(label)
		if id == "" || strings.ContainsAny(id, " :") {
			continue
		}
		if label == "" {
			label = id
		}
		list = append(list, Model{ID: id, Label: label, Current: true})
	}
	return list, nil
}
