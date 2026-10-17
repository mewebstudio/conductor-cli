package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/fsutil"
	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/skills"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

// exportItem is one thing an export can carry.
type exportItem struct {
	name  string // name inside the export folder
	src   func() string
	local bool // machine-only; included with --include-local
	dir   bool
}

var exportItems = []exportItem{
	{name: "conductor.toml", src: paths.ConfigFile},
	{name: "skills.toml", src: paths.SkillsFile},
	{name: "providers", src: paths.ProvidersDir, dir: true},
	{name: "local.toml", src: paths.LocalFile, local: true},
	{name: "skills.local.toml", src: paths.LocalSkillsFile, local: true},
}

const exportReadme = "EXPORT.md"

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func exportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <folder>",
		Short: "Copy all your settings into a folder (backup or transfer); never includes API keys",
		Long: "Writes conductor.toml, skills.toml (your agents, skills and plugins) and provider\n" +
			"plugins into <folder>. --include-local adds this machine's overrides and local-folder\n" +
			"skills. API keys stay in the OS credential store and are never exported.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dst, err := paths.Expand(args[0])
			if err != nil {
				return err
			}
			withLocal, _ := cmd.Flags().GetBool("include-local")
			force, _ := cmd.Flags().GetBool("force")
			if entries, _ := os.ReadDir(dst); len(entries) > 0 && !force && !exists(filepath.Join(dst, exportReadme)) {
				return fmt.Errorf("%s is not empty (use --force, or an empty folder)", tilde(dst))
			}
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			var written []string
			for _, it := range exportItems {
				if it.local && !withLocal {
					continue
				}
				src := it.src()
				if !exists(src) {
					continue
				}
				target := filepath.Join(dst, it.name)
				if it.dir {
					if entries, _ := os.ReadDir(src); len(entries) == 0 {
						continue
					}
					if err := os.RemoveAll(target); err != nil {
						return err
					}
					if err := fsutil.CopyTree(src, target, true); err != nil {
						return err
					}
				} else {
					b, err := os.ReadFile(src)
					if err != nil {
						return err
					}
					if err := os.WriteFile(target, b, 0o644); err != nil {
						return err
					}
				}
				written = append(written, it.name)
				ui.Okf("%s", it.name)
			}
			if len(written) == 0 {
				return errors.New("nothing to export yet; run `conductor install` first")
			}
			host, _ := os.Hostname()
			readme := fmt.Sprintf("# Conductor settings export\n\n- Exported: %s\n- From: %s\n- Conductor: %s\n- Contents: %s\n\n"+
				"API keys are not included; enter them again with `conductor providers key <name>`.\n\n"+
				"Restore with:\n\n    conductor import <this folder>\n",
				time.Now().Format(time.RFC3339), host, version, strings.Join(written, ", "))
			if err := os.WriteFile(filepath.Join(dst, exportReadme), []byte(readme), 0o644); err != nil {
				return err
			}
			ui.Linef("Exported to %s (no API keys).", tilde(dst))
			return nil
		},
	}
	cmd.Flags().Bool("include-local", false, "also export this machine's overrides and local-folder skills")
	cmd.Flags().Bool("force", false, "write into a non-empty folder")
	return cmd
}

func importCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <folder>",
		Short: "Restore settings from a folder made by `conductor export`",
		Long: "Replaces your settings with the exported ones (current files are backed up to\n" +
			"~/.conductor-cli/backups), re-applies Claude files and installs the listed skills.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := paths.Expand(args[0])
			if err != nil {
				return err
			}
			if !exists(filepath.Join(src, "conductor.toml")) && !exists(filepath.Join(src, "skills.toml")) {
				return fmt.Errorf("%s does not look like a conductor export", tilde(src))
			}
			// Validate before touching anything.
			if exists(filepath.Join(src, "conductor.toml")) {
				if _, err := config.ReadTree(filepath.Join(src, "conductor.toml")); err != nil {
					return fmt.Errorf("invalid conductor.toml: %w", err)
				}
			}
			yes, _ := cmd.Flags().GetBool("yes")
			if !yes && ui.Interactive() {
				ok, err := ui.Confirm("Replace your current settings with "+tilde(src)+"?", "Current files are backed up first.", true)
				if err != nil || !ok {
					return err
				}
			}
			if err := paths.EnsureDirs(); err != nil {
				return err
			}
			for _, it := range exportItems {
				from := filepath.Join(src, it.name)
				if !exists(from) {
					continue
				}
				to := it.src()
				if it.dir {
					entries, _ := os.ReadDir(from)
					for _, e := range entries {
						t := filepath.Join(to, e.Name())
						if exists(t) {
							if _, err := fsutil.Backup(filepath.Join(t, "provider.toml")); err != nil {
								return err
							}
							if err := os.RemoveAll(t); err != nil {
								return err
							}
						}
						if err := fsutil.CopyTree(filepath.Join(from, e.Name()), t, true); err != nil {
							return err
						}
					}
				} else {
					b, err := os.ReadFile(from)
					if err != nil {
						return err
					}
					if _, err := fsutil.WriteIfChanged(to, b, 0o644); err != nil {
						return err
					}
				}
				ui.Okf("%s", it.name)
			}
			if _, err := config.Load(); err != nil {
				return fmt.Errorf("imported settings are invalid: %w", err)
			}
			if _, err := skills.Load(); err != nil {
				return fmt.Errorf("imported skills list is invalid: %w", err)
			}
			if err := apply(false); err != nil {
				return err
			}
			if err := ensureKeys(cmd, ui.Interactive()); err != nil {
				return err
			}
			if err := runSkillSync(cmd.Context(), false); err != nil {
				ui.Warnf("some skills failed; run `conductor skills sync` to retry")
			}
			if configIsRepo() {
				ui.Linef("Run `conductor sync` to push the imported settings to your settings repository.")
			}
			return nil
		},
	}
	cmd.Flags().Bool("yes", false, "do not ask for confirmation")
	return cmd
}
