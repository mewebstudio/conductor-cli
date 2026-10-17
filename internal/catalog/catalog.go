// Package catalog knows the models, effort levels, roles and profiles conductor offers.
package catalog

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mewebstudio/conductor-cli/internal/models"
)

// Role describes one model slot.
type Role struct {
	Key         string // config key under [models]
	Agent       string // generated agent name ("" for the lead)
	Label       string
	Summary     string
	Optional    bool // can be disabled
	FallbackFor string
}

// Roles in menu order.
var Roles = []Role{
	{Key: "lead", Label: "Lead", Summary: "Main session: plans, delegates, verifies. Writes no code."},
	{Key: "mini", Agent: "worker-mini", Label: "Mini worker", Summary: "Mechanical work with no judgment: find, list, count, run and summarize.", Optional: true, FallbackFor: "standard"},
	{Key: "standard", Agent: "worker-standard", Label: "Standard worker", Summary: "Clear-scope implementation, tests, docs, routine production work."},
	{Key: "expert", Agent: "worker-expert", Label: "Expert worker", Summary: "Architecture, complex logic, unknown bugs, security-sensitive code."},
	{Key: "reviewer", Agent: "reviewer", Label: "Claude reviewer", Summary: "Read-only review; last link of the review chain."},
}

// FindRole returns a role by key.
func FindRole(key string) (Role, bool) {
	for _, r := range Roles {
		if r.Key == key {
			return r, true
		}
	}
	return Role{}, false
}

// ModelLabel returns a human label for a Claude model id.
func ModelLabel(id string) string {
	list, _ := models.Claude()
	if m, ok := models.Find(list, id); ok {
		return m.Label
	}
	return id
}

// ValidateRole checks a model/effort pair for a role against the models
// Claude Code offers your account.
func ValidateRole(role, model, effort string, force bool) error {
	if _, ok := FindRole(role); !ok {
		return fmt.Errorf("unknown role %q (lead, mini, standard, expert, reviewer)", role)
	}
	if model == "" {
		return fmt.Errorf("model is required")
	}
	list, _ := models.Claude()
	m, known := models.Find(list, model)
	switch {
	case !known && !force:
		var current []string
		for _, x := range list {
			if x.Current {
				current = append(current, x.ID)
			}
		}
		return fmt.Errorf("unknown Claude model %q; available: %s (see `conductor models available`, or --force)", model, strings.Join(current, ", "))
	case known && !m.Supports(effort):
		if len(m.Efforts) == 0 {
			return fmt.Errorf("%s has no effort levels; leave effort empty", model)
		}
		return fmt.Errorf("%s supports effort %s", model, strings.Join(m.Efforts, ", "))
	case !known && effort != "" && !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, effort):
		return fmt.Errorf("invalid effort %q", effort)
	}
	// The lead's effort lives in settings.json, which does not accept "max".
	if role == "lead" && effort == "max" {
		return fmt.Errorf("the lead cannot use max effort (Claude settings accept up to xhigh)")
	}
	return nil
}

// Assignment is a model/effort pair.
type Assignment struct{ Model, Effort string }

// Profile is a named set of role assignments.
type Profile struct {
	Name    string
	Summary string
	Roles   map[string]Assignment
}

// Profiles in menu order.
var Profiles = []Profile{
	{
		Name: "economy", Summary: "Saves quota: no Opus workers.",
		Roles: map[string]Assignment{
			"lead":     {"claude-opus-5-5", "medium"},
			"mini":     {"claude-haiku-5-5", ""},
			"standard": {"claude-sonnet-5-5", "medium"},
			"expert":   {"claude-sonnet-5-5", "high"},
			"reviewer": {"claude-sonnet-5-5", "high"},
		},
	},
	{
		Name: "balanced", Summary: "Default: Opus lead, Sonnet for routine, Opus for hard work.",
		Roles: map[string]Assignment{
			"lead":     {"claude-opus-5-5", "high"},
			"mini":     {"claude-haiku-5-5", ""},
			"standard": {"claude-sonnet-5-5", "high"},
			"expert":   {"claude-opus-5-5", "medium"},
			"reviewer": {"claude-opus-5-5", "medium"},
		},
	},
	{
		Name: "quality", Summary: "Critical work when quota is plentiful.",
		Roles: map[string]Assignment{
			"lead":     {"claude-opus-5-5", "high"},
			"mini":     {"claude-sonnet-5-5", ""},
			"standard": {"claude-sonnet-5-5", "high"},
			"expert":   {"claude-opus-5-5", "high"},
			"reviewer": {"claude-opus-5-5", "high"},
		},
	},
}

// FindProfile returns a profile by name.
func FindProfile(name string) (Profile, bool) {
	for _, p := range Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}
