package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mewebstudio/conductor-cli/internal/catalog"
	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/models"
	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/providers"
	"github.com/mewebstudio/conductor-cli/internal/secrets"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

func roleOf(c *config.Config, key string) config.Role {
	switch key {
	case "lead":
		r := c.Models.Lead
		r.Enabled = true
		return r
	case "mini":
		return c.Models.Mini
	case "standard":
		return c.Models.Standard
	case "expert":
		return c.Models.Expert
	case "reviewer":
		return c.Models.Reviewer
	}
	return config.Role{}
}

func modelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Show or change which model and effort each role uses",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			ui.Linef("Profile: %s\n", c.Profile)
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "ROLE\tAGENT\tMODEL\tEFFORT\tSTATE")
			for _, r := range catalog.Roles {
				role := roleOf(c, r.Key)
				state := "on"
				if !role.Enabled {
					state = "off → " + r.FallbackFor
				}
				agent := r.Agent
				if agent == "" {
					agent = "(main session)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Key, agent, role.Model, orDash(role.Effort), state)
			}
			return w.Flush()
		},
	}
	set := editFlags(&cobra.Command{
		Use:     "set <role> <model> [effort]",
		Short:   "Set a role's model and effort (roles: lead, mini, standard, expert, reviewer)",
		Example: "  conductor models set standard claude-sonnet-5-5 high\n  conductor models set mini claude-haiku-5-5 low",
		Args:    cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			effort := ""
			if len(args) == 3 {
				effort = args[2]
			}
			force, _ := cmd.Flags().GetBool("force")
			if err := setRole(args[0], args[1], effort, force, isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	set.Flags().Bool("force", false, "accept a model id conductor does not know")

	toggle := func(on bool) *cobra.Command {
		name, short := "disable", "Turn an optional role off (mini → work goes to standard; reviewer)"
		if on {
			name, short = "enable", "Turn an optional role back on"
		}
		return editFlags(&cobra.Command{
			Use:   name + " <mini|reviewer>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if args[0] != "mini" && args[0] != "reviewer" {
					return fmt.Errorf("only mini and reviewer can be switched %s", map[bool]string{true: "on", false: "off"}[on])
				}
				if err := config.SetValue("models."+args[0]+".enabled", on, isLocal(cmd)); err != nil {
					return err
				}
				return changed(cmd)
			},
		})
	}

	edit := editFlags(&cobra.Command{
		Use:   "edit",
		Short: "Choose every role's model and effort from menus",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := editRoles(isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})

	reset := editFlags(&cobra.Command{
		Use:   "reset",
		Short: "Go back to the recommended (balanced) profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := applyProfile("balanced", isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	available := &cobra.Command{
		Use:   "available [claude|codex|antigravity|nvidia]",
		Short: "List the models each tool offers you (read from the tools themselves)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			want := ""
			if len(args) == 1 {
				want = args[0]
			}
			show := func(name, source string, list []models.Model) {
				if want != "" && want != name {
					return
				}
				ui.Linef("%s (%s)", name, source)
				for _, m := range list {
					eff := strings.Join(m.Efforts, " ")
					if eff == "" {
						eff = "-"
					}
					old := ""
					if !m.Current {
						old = "  (older)"
					}
					ui.Linef("  %-30s %-26s effort: %s%s", m.ID, m.Label, eff, old)
				}
				ui.Linef("")
			}
			cl, ok := models.Claude()
			show("claude", map[bool]string{true: "Claude Code catalog for your account", false: "built-in list; open Claude Code once to refresh"}[ok], cl)
			cx, ok := models.Codex()
			show("codex", map[bool]string{true: "Codex CLI catalog", false: "built-in list; run codex once to refresh"}[ok], cx)
			if want == "" || want == "antigravity" {
				if ag, err := models.Antigravity(cmd.Context()); err == nil {
					show("antigravity", "agy models", ag)
				} else if want == "antigravity" {
					return fmt.Errorf("antigravity: %v", err)
				}
			}
			if want == "" || want == "nvidia" {
				if key, err := secrets.Get("nvidia"); err == nil {
					ids, err := providers.NVIDIAModels(cmd.Context(), key)
					if err != nil {
						return fmt.Errorf("nvidia: %v", err)
					}
					var list []models.Model
					for _, id := range ids {
						list = append(list, models.Model{ID: id, Label: "", Current: true})
					}
					show("nvidia", "NVIDIA API", list)
				} else if want == "nvidia" {
					return fmt.Errorf("nvidia: no API key stored (`conductor providers key nvidia`)")
				}
			}
			return nil
		},
	}
	cmd.AddCommand(set, toggle(true), toggle(false), edit, reset, available)
	return cmd
}

func setRole(role, model, effort string, force, local bool) error {
	if err := catalog.ValidateRole(role, model, effort, force); err != nil {
		return err
	}
	if err := config.SetValue("models."+role+".model", model, local); err != nil {
		return err
	}
	if err := config.SetValue("models."+role+".effort", effort, local); err != nil {
		return err
	}
	return config.SetValue("profile", "custom", local)
}

func applyProfile(name string, local bool) error {
	p, ok := catalog.FindProfile(name)
	if !ok {
		return fmt.Errorf("unknown profile %q (economy, balanced, quality)", name)
	}
	for role, a := range p.Roles {
		if err := config.SetValue("models."+role+".model", a.Model, local); err != nil {
			return err
		}
		if err := config.SetValue("models."+role+".effort", a.Effort, local); err != nil {
			return err
		}
	}
	if err := config.SetValue("profile", name, local); err != nil {
		return err
	}
	if !local {
		warnLocalModelOverrides()
	}
	return nil
}

// warnLocalModelOverrides points out machine-only settings that still win
// over a synced change.
func warnLocalModelOverrides() {
	tree, err := config.ReadTree(paths.LocalFile())
	if err != nil {
		return
	}
	var keys []string
	for _, l := range config.Flatten(tree) {
		k := strings.SplitN(l, " = ", 2)[0]
		if k == "profile" || strings.HasPrefix(k, "models.") {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		ui.Warnf("this machine overrides %s; remove with `conductor config unset %s --local`", k, k)
	}
}

func modelOptions() []ui.Option {
	list, _ := models.Claude()
	var cur, old []ui.Option
	for _, m := range list {
		o := ui.Option{Label: fmt.Sprintf("%-12s %s", m.Label, m.ID), Value: m.ID}
		if m.Current {
			cur = append(cur, o)
		} else {
			o.Label += "  (older)"
			old = append(old, o)
		}
	}
	return append(cur, old...)
}

// effortOptions lists the effort levels a model supports.
func effortOptions(model string, lead bool) []ui.Option {
	list, _ := models.Claude()
	levels := []string{"low", "medium", "high", "xhigh", "max"}
	if m, ok := models.Find(list, model); ok {
		levels = m.Efforts
	}
	opts := []ui.Option{{Label: "model default", Value: ""}}
	for _, e := range levels {
		if lead && e == "max" {
			continue
		}
		opts = append(opts, ui.Option{Label: e, Value: e})
	}
	return opts
}

// editRoles walks every role through model and effort menus.
func editRoles(local bool) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	for _, r := range catalog.Roles {
		cur := roleOf(c, r.Key)
		if r.Optional {
			on, err := ui.Confirm("Use the "+r.Label+"?", r.Summary, cur.Enabled)
			if err != nil {
				return err
			}
			if err := config.SetValue("models."+r.Key+".enabled", on, local); err != nil {
				return err
			}
			if !on {
				continue
			}
		}
		model, err := ui.Select(r.Label+": model", r.Summary, modelOptions(), cur.Model)
		if err != nil {
			return err
		}
		effort, err := ui.Select(r.Label+": effort", "Higher effort thinks longer and uses more quota.", effortOptions(model, r.Key == "lead"), cur.Effort)
		if err != nil {
			return err
		}
		if err := setRole(r.Key, model, effort, true, local); err != nil {
			return err
		}
	}
	return nil
}

func profileCmd() *cobra.Command {
	return editFlags(&cobra.Command{
		Use:   "profile [economy|balanced|quality]",
		Short: "Show profiles or switch every role at once",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if err := applyProfile(args[0], isLocal(cmd)); err != nil {
					return err
				}
				return changed(cmd)
			}
			c, err := config.Load()
			if err != nil {
				return err
			}
			for _, p := range catalog.Profiles {
				mark := "  "
				if p.Name == c.Profile {
					mark = "▶ "
				}
				ui.Linef("%s%-9s %s", mark, p.Name, p.Summary)
			}
			if c.Profile == "custom" {
				ui.Linef("▶ custom    (edited by hand; see `conductor models`)")
			}
			return nil
		},
	})
}
