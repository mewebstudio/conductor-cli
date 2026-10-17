package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mewebstudio/conductor-cli/internal/claude"
	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/fsutil"
	"github.com/mewebstudio/conductor-cli/internal/notify"
	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/providers"
	"github.com/mewebstudio/conductor-cli/internal/review"
	"github.com/mewebstudio/conductor-cli/internal/secrets"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

func askCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ask --prompt <file> --out <file> [--provider <name>]",
		Short: "Send a text-only job (translation, summary, test ideas) to an external model",
		Long: "Runs a text-only job on an enabled provider, saving Claude quota.\n" +
			"Without --provider it prefers key-based providers and plugins, then Codex.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			prompt, _ := cmd.Flags().GetString("prompt")
			out, _ := cmd.Flags().GetString("out")
			name, _ := cmd.Flags().GetString("provider")
			model, _ := cmd.Flags().GetString("model")
			var candidates []providers.Provider
			if name != "" {
				p, err := providers.Get(name)
				if err != nil {
					return err
				}
				candidates = []providers.Provider{p}
			} else {
				var codex []providers.Provider
				for _, p := range providers.All() {
					if p.Name() == "codex" {
						codex = append(codex, p)
					} else {
						candidates = append(candidates, p)
					}
				}
				candidates = append(candidates, codex...)
			}
			wd, _ := os.Getwd()
			root := review.ProjectRoot(wd)
			for _, p := range candidates {
				if !providers.Has(p, "ask") || providers.Ready(c, p) != nil {
					continue
				}
				if !review.WithinLimit(c, p.Name()) {
					fmt.Printf("attempt provider=%s status=skipped detail=%q\n", p.Name(), "daily limit reached")
					continue
				}
				rv := c.Reviewer(p.Name())
				if model != "" {
					rv.Model, rv.Effort = model, ""
				}
				st, err := p.Run(cmd.Context(), providers.Request{
					Capability: "ask", PromptFile: prompt, OutputFile: out,
					Root: root, Model: rv.Model, Effort: rv.Effort,
				})
				if st != providers.NotConfigured {
					review.RecordUse(p.Name())
				}
				if st == providers.OK {
					fmt.Printf("status=ok provider=%s output=%s\n", p.Name(), out)
					return nil
				}
				fmt.Printf("attempt provider=%s status=%s detail=%q\n", p.Name(), st, fmt.Sprint(err))
			}
			fmt.Println("status=failed")
			return exitCode(4)
		},
	}
	cmd.Flags().String("prompt", "", "file with the job (required)")
	cmd.Flags().String("out", "", "file to write the answer to (required)")
	cmd.Flags().String("provider", "", "use this provider only")
	cmd.Flags().String("model", "", "use this model instead of the provider's configured one (see `conductor models available`)")
	_ = cmd.MarkFlagRequired("prompt")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func notifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "notify <message>",
		Short: "Show a desktop notification (used by the lead when a decision is needed)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			if c.Comms.Notify == "off" {
				return nil
			}
			if err := notify.Send("Conductor", strings.Join(args, " ")); err != nil {
				ui.Warnf("notification not shown: %v", err)
			}
			return nil
		},
	}
}

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show every setting, or change one",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			tree, err := config.EffectiveTree()
			if err != nil {
				return err
			}
			local, err := config.ReadTree(paths.LocalFile())
			if err != nil {
				return err
			}
			localKeys := map[string]bool{}
			for _, l := range config.Flatten(local) {
				localKeys[strings.SplitN(l, " = ", 2)[0]] = true
			}
			for _, l := range config.Flatten(tree) {
				if localKeys[strings.SplitN(l, " = ", 2)[0]] {
					l += "   # this machine only"
				}
				ui.Linef("%s", l)
			}
			return nil
		},
	}
	set := editFlags(&cobra.Command{
		Use:     "set <key> <value>",
		Short:   "Change one setting (lists: comma or space separated)",
		Example: "  conductor config set git.push ask\n  conductor config set comms.address Alex\n  conductor config set parallel.max_workers 3 --local",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.HasPrefix(args[0], "models.") || args[0] == "review.chain" {
				return errors.New("use `conductor models` or `conductor review` for this setting; they validate it")
			}
			if err := config.SetString(args[0], args[1], isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	unset := editFlags(&cobra.Command{
		Use:   "unset <key>",
		Short: "Remove a setting so the default (or synced value) applies again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.UnsetValue(args[0], isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	path := &cobra.Command{
		Use:   "path",
		Short: "Print where settings are stored",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			ui.Linef("synced:  %s", config.Target(false))
			ui.Linef("local:   %s", config.Target(true))
		},
	}
	move := &cobra.Command{
		Use:   "move <folder>",
		Short: "Move your settings to another folder (e.g. iCloud Drive) and use it from now on",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dst, err := paths.Expand(args[0])
			if err != nil {
				return err
			}
			src := paths.ConfigDir()
			if filepath.Clean(src) == filepath.Clean(dst) {
				return fmt.Errorf("settings are already in %s", tilde(dst))
			}
			if entries, _ := os.ReadDir(dst); len(entries) > 0 {
				if _, err := os.Stat(filepath.Join(dst, "conductor.toml")); err != nil {
					return fmt.Errorf("%s is not empty", tilde(dst))
				}
				ui.Warnf("%s already holds settings; using them (yours stay in %s)", tilde(dst), tilde(src))
			} else if err := fsutil.CopyTree(src, dst, false); err != nil {
				return err
			}
			if err := paths.SetConfigDir(dst); err != nil {
				return err
			}
			ui.Okf("settings now live in %s (the old folder %s was left in place)", tilde(dst), tilde(src))
			return apply(false)
		},
	}
	cmd.AddCommand(set, unset, path, move)
	return cmd
}

func commsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "comms",
		Short: "Show or change the report language and how agents address you",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			addr := c.Comms.Address
			if addr == "" {
				addr = "(none — agents say \"the user\")"
			}
			ui.Linef("Language: %s", c.Comms.Language)
			ui.Linef("Address:  %s", addr)
			ui.Linef("Notify:   %s", c.Comms.Notify)
			return nil
		},
	}
	lang := editFlags(&cobra.Command{
		Use:   "language <en|tr>",
		Short: "Set the language agents use when reporting to you",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.SetString("comms.language", args[0], isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	addr := editFlags(&cobra.Command{
		Use:     "address [name]",
		Short:   "Set how agents address you; no name (or --clear) removes it",
		Example: "  conductor comms address Alex\n  conductor comms address --clear",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clear, _ := cmd.Flags().GetBool("clear")
			if len(args) == 0 && !clear {
				return fmt.Errorf("give a name, or --clear to remove it")
			}
			v := ""
			if len(args) == 1 {
				v = strings.TrimSpace(args[0])
			}
			if err := config.SetValue("comms.address", v, isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	addr.Flags().Bool("clear", false, "remove the form of address")
	edit := editFlags(&cobra.Command{
		Use:   "edit",
		Short: "Choose language and form of address from menus",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if isLocal(cmd) {
				return fmt.Errorf("use `comms language` / `comms address` with --local")
			}
			if err := askIdentity(); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	cmd.AddCommand(lang, addr, edit)
	return cmd
}

func uninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the skill, agents, hook and CLAUDE.md block conductor installed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reset, _ := cmd.Flags().GetBool("reset-model")
			purge, _ := cmd.Flags().GetBool("purge")
			changes, err := claude.Uninstall(reset)
			if err != nil {
				return err
			}
			for _, ch := range changes {
				if ch.Action != "unchanged" {
					ui.Okf("%-9s %s", ch.Action, tilde(ch.Path))
				}
			}
			if purge {
				for _, p := range providers.All() {
					if p.NeedsKey() {
						_ = secrets.Delete(p.Name())
					}
				}
				if err := os.RemoveAll(paths.Root()); err != nil {
					return err
				}
				ui.Okf("removed %s and stored API keys", tilde(paths.Root()))
				return nil
			}
			ui.Linef("Settings kept in %s (use --purge to remove them and stored keys).", tilde(paths.Root()))
			ui.Linef("Backups of changed files are in %s.", tilde(paths.BackupsDir()))
			return nil
		},
	}
	cmd.Flags().Bool("reset-model", false, "also remove the lead model/effort from settings.json")
	cmd.Flags().Bool("purge", false, "also delete ~/.conductor-cli and stored API keys")
	return cmd
}
