package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mewebstudio/conductor-cli/internal/catalog"
	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/providers"
	"github.com/mewebstudio/conductor-cli/internal/secrets"
	"github.com/mewebstudio/conductor-cli/internal/skills"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

func installCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Set up conductor on this machine (safe to run again)",
		Long: "Creates ~/.conductor-cli, connects your settings repository, asks for your choices\n" +
			"(quick or custom), then installs the skill, agents and Claude settings.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()
			url, _ := cmd.Flags().GetString("config-repo")
			cpath, _ := cmd.Flags().GetString("config-path")
			yes, _ := cmd.Flags().GetBool("yes")
			custom, _ := cmd.Flags().GetBool("custom")
			interactive := ui.Interactive() && !yes

			if err := paths.EnsureDirs(); err != nil {
				return err
			}
			ui.Linef("Conductor %s\n", version)

			if cpath != "" {
				dir, err := paths.Expand(cpath)
				if err != nil {
					return err
				}
				if err := paths.SetConfigDir(dir); err != nil {
					return err
				}
				ui.Okf("settings folder: %s", tilde(dir))
			}
			if url == "" && cpath == "" && interactive && !configIsRepo() {
				var err error
				url, err = ui.Input("Settings repository (optional)",
					"A private git repo (e.g. github.com/you/conductor-config) keeps your choices in sync across machines. Leave empty to skip.", "")
				if err != nil {
					return err
				}
			}
			if err := setupConfigRepo(ctx, url); err != nil {
				return err
			}

			// A first setup that is aborted (Ctrl+C) or fails before the
			// questions are done must not leave half a settings file behind:
			// the next install would offer to reuse it and skip the questions.
			fresh, answered := !config.Exists(), false
			defer func() {
				if fresh && !answered {
					_ = os.Remove(paths.ConfigFile())
					_ = os.Remove(paths.SkillsFile())
					if err != nil {
						ui.Warnf("setup did not finish; no settings were kept. Run `conductor install` again.")
					}
				}
			}()

			reuse := false
			if config.Exists() {
				reuse = true
				if interactive {
					var err error
					reuse, err = ui.Confirm("Use the settings already in your settings folder?",
						"Choose No to go through the questions again.", true)
					if err != nil {
						return err
					}
				}
			}

			if !reuse {
				if err := config.SetValue("profile", "balanced", false); err != nil {
					return err
				}
				if interactive {
					if !custom {
						mode, err := ui.Select("How do you want to set up?", "", []ui.Option{
							{Label: "Quick — recommended defaults (Opus 5.5 lead, Sonnet 5.5 / Opus 5.5 workers, Codex review)", Value: "quick"},
							{Label: "Custom — choose every setting", Value: "custom"},
						}, "quick")
						if err != nil {
							return err
						}
						custom = mode == "custom"
					}
					if err := wizard(cmd, custom); err != nil {
						return err
					}
				}
			}
			answered = true

			if err := ensureKeys(cmd, interactive); err != nil {
				return err
			}
			if err := apply(false); err != nil {
				return err
			}
			if err := installSkills(cmd, interactive); err != nil {
				return err
			}
			ui.Linef("\nInstalled. Open a new Claude Code session to use it. Next:")
			ui.Linef("  conductor doctor     check everything")
			if configIsRepo() {
				ui.Linef("  conductor sync       save your choices to the settings repository")
			}
			ui.Linef("  conductor --help     all commands")
			return nil
		},
	}
	cmd.Flags().String("config-repo", "", "git URL of your private settings repository")
	cmd.Flags().String("config-path", "", "keep settings in this folder (e.g. iCloud/Dropbox, or an existing clone)")
	cmd.Flags().Bool("yes", false, "no questions: use defaults (or the settings repository)")
	cmd.Flags().Bool("custom", false, "go straight to the custom setup")
	return cmd
}

// wizard asks the setup questions; quick mode only asks who you are and
// which optional providers to add.
func wizard(cmd *cobra.Command, custom bool) error {
	if err := askIdentity(); err != nil {
		return err
	}
	if custom {
		opts := make([]ui.Option, len(catalog.Profiles))
		for i, p := range catalog.Profiles {
			opts[i] = ui.Option{Label: fmt.Sprintf("%-9s %s", p.Name, p.Summary), Value: p.Name}
		}
		prof, err := ui.Select("Profile", "Sets every role at once; you can fine-tune next.", opts, "balanced")
		if err != nil {
			return err
		}
		if err := applyProfile(prof, false); err != nil {
			return err
		}
		tune, err := ui.Confirm("Fine-tune each role's model and effort?", "", false)
		if err != nil {
			return err
		}
		if tune {
			if err := editRoles(false); err != nil {
				return err
			}
		}
	}

	if err := askOptionalProviders(cmd); err != nil {
		return err
	}
	if err := askVault(); err != nil {
		return err
	}
	if !custom {
		return nil
	}
	if err := editReview(false); err != nil {
		return err
	}

	type q struct {
		key, title string
		opts       []ui.Option
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	cur := func(key string) string {
		tree, _ := config.EffectiveTree()
		v, _ := config.Lookup(tree, key)
		return fmt.Sprint(v)
	}
	for _, x := range []q{
		{"git.commit", "When may the lead commit?", []ui.Option{
			{Label: "ask — prepare the commit, wait for my OK", Value: "ask"},
			{Label: "milestones — commit automatically at milestones", Value: "milestones"},
			{Label: "every-step — commit after every verified step", Value: "every-step"},
			{Label: "manual — never; I commit myself", Value: "manual"},
		}},
		{"git.push", "Push?", []ui.Option{
			{Label: "never — unless I ask", Value: "never"},
			{Label: "ask — after my OK", Value: "ask"},
			{Label: "auto — after each commit", Value: "auto"},
		}},
		{"git.commit_language", "Commit message language", []ui.Option{{Label: "English", Value: "en"}, {Label: "Türkçe", Value: "tr"}}},
		{"lead.coding", "May the lead edit files itself?", []ui.Option{
			{Label: "small-edits — only trivial, logic-free edits", Value: "small-edits"},
			{Label: "strict — never; everything goes to workers", Value: "strict"},
		}},
		{"comms.notify", "Desktop notifications", []ui.Option{
			{Label: "decisions — when a decision is needed", Value: "decisions"},
			{Label: "all — decisions and finished tasks", Value: "all"},
			{Label: "off", Value: "off"},
		}},
	} {
		v, err := ui.Select(x.title, "", x.opts, cur(x.key))
		if err != nil {
			return err
		}
		if err := config.SetValue(x.key, v, false); err != nil {
			return err
		}
	}
	workers, err := ui.Select("Parallel workers at most", "More is faster but uses quota faster.", []ui.Option{
		{Label: "1", Value: "1"}, {Label: "2", Value: "2"}, {Label: "3", Value: "3"}, {Label: "4", Value: "4"},
	}, strconv.Itoa(c.Parallel.MaxWorkers))
	if err != nil {
		return err
	}
	if err := config.SetString("parallel.max_workers", workers, false); err != nil {
		return err
	}
	auto, err := ui.Confirm("Sync settings automatically when a Claude session starts?", "Pull-only and silent; pushing stays manual (`conductor sync`).", c.Sync.Auto)
	if err != nil {
		return err
	}
	return config.SetValue("sync.auto", auto, false)
}

// askIdentity asks for the report language and how agents should address you.
func askIdentity() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	lang, err := ui.Select("Language for reports to you", "Code, files and commits stay in English unless you change it.",
		[]ui.Option{{Label: "English", Value: "en"}, {Label: "Türkçe", Value: "tr"}}, c.Comms.Language)
	if err != nil {
		return err
	}
	if err := config.SetValue("comms.language", lang, false); err != nil {
		return err
	}
	addr, err := ui.Input("How should the agents address you? (optional)",
		"A name or title, e.g. Alex. Leave empty for none.", c.Comms.Address)
	if err != nil {
		return err
	}
	return config.SetValue("comms.address", addr, false)
}

// askVault offers one Obsidian vault for every project's docs.
func askVault() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	on, err := ui.Confirm("Use one Obsidian vault for all your projects?",
		"Each project's docs folder (decisions, handoff, reviews) is linked into the vault; files stay in the project.", c.Obsidian.Enabled)
	if err != nil || !on {
		if err == nil && c.Obsidian.Enabled {
			err = config.SetValue("obsidian.enabled", false, false)
		}
		return err
	}
	def := c.Obsidian.Vault
	if def == "" {
		def = "~/Documents/Conductor"
	}
	v, err := ui.Input("Vault folder", "Created if missing. Use a synced folder (iCloud, Dropbox) to see it on every machine.", def)
	if err != nil {
		return err
	}
	abs, err := paths.Expand(v)
	if err != nil {
		return err
	}
	if err := config.SetValue("obsidian.vault", portable(abs), false); err != nil {
		return err
	}
	if err := config.SetValue("obsidian.enabled", true, false); err != nil {
		return err
	}
	return relinkVault()
}

// askOptionalProviders offers every provider that is off by default.
func askOptionalProviders(cmd *cobra.Command) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	for _, p := range providers.All() {
		if c.ProviderEnabled(p.Name()) || p.Name() == "codex" {
			continue
		}
		if !p.NeedsKey() && p.Check() != nil {
			continue // a CLI-based provider that is not installed here
		}
		on, err := ui.Confirm("Add "+p.Name()+"?", p.Description()+". Optional; you can add it later with `conductor providers enable "+p.Name()+"`.", false)
		if err != nil {
			return err
		}
		if on {
			if err := enableProvider(cmd.Context(), p.Name(), false); err != nil {
				ui.Warnf("%s not enabled: %v", p.Name(), err)
			}
		}
	}
	return nil
}

// ensureKeys makes sure enabled providers have keys on this machine.
func ensureKeys(cmd *cobra.Command, interactive bool) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	for _, p := range providers.All() {
		if !c.ProviderEnabled(p.Name()) || !p.NeedsKey() || secrets.Has(p.Name()) {
			continue
		}
		if !interactive {
			ui.Warnf("%s is enabled but has no API key on this machine; run `conductor providers key %s`", p.Name(), p.Name())
			continue
		}
		ui.Linef("%s is enabled in your settings but has no API key on this machine.", p.Name())
		if _, err := askKey(cmd.Context(), p.Name(), true); err != nil {
			ui.Warnf("%s: %v", p.Name(), err)
		}
	}
	return nil
}

// installSkills chooses agents, offers to import what is already installed,
// installs the skills in your settings and lets you add more.
func installSkills(cmd *cobra.Command, interactive bool) error {
	ctx := cmd.Context()
	sel, err := skills.Load()
	if err != nil {
		return err
	}
	ui.Linef("\nSkills")
	if interactive && len(sel.Agents) == 0 {
		if err := chooseAgents(); err != nil {
			return err
		}
	}
	if interactive {
		if ex := skills.FindExisting(sel); len(ex.Skills)+len(ex.Plugins) > 0 {
			ok, err := ui.Confirm(fmt.Sprintf("Found %d skill(s) and %d plugin(s) already on this machine. Add them to your list?", len(ex.Skills), len(ex.Plugins)),
				"So your other machines get them too. You can pick which.", true)
			if err != nil {
				return err
			}
			if ok {
				if err := importExisting(false); err != nil {
					return err
				}
			}
		}
	}
	if sel, err = skills.Load(); err != nil {
		return err
	}
	if skills.Pending() {
		run := true
		if interactive {
			if run, err = ui.Confirm(fmt.Sprintf("Install the %d skill(s) and %d plugin(s) in your settings?", len(sel.Skills), len(sel.Plugins)), "Missing ones are downloaded and linked to your agents.", true); err != nil {
				return err
			}
		}
		if run {
			if err := runSkillSync(ctx, false); err != nil {
				ui.Warnf("some skills failed; run `conductor skills sync` to retry")
			}
		}
	}
	if !interactive {
		return nil
	}
	for {
		q, err := ui.Input("Add a skill? (optional)",
			"Search words, a skill name, owner/repo, plugin@marketplace or a folder. Leave empty to finish.", "")
		if err != nil {
			return err
		}
		if q == "" {
			return nil
		}
		if strings.Contains(q, " ") {
			if err := searchAndAdd(ctx, q, true); err != nil {
				ui.Warnf("%v", err)
			}
			continue
		}
		if err := addSkills(ctx, []string{q}, false); err != nil {
			// A bare word that is not an exact skill name: offer a search instead.
			if err := searchAndAdd(ctx, q, true); err != nil {
				ui.Warnf("%v", err)
			}
		}
	}
}
