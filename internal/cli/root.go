// Package cli implements the conductor command line.
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
	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

var version string

// Execute runs the CLI and returns the process exit code.
func Execute(v string) int {
	version = v
	root := &cobra.Command{
		Use:           "conductor",
		Short:         "Lead/worker orchestration for Claude Code, with Codex and other reviewers",
		Long:          "Conductor sets up a lead Claude session that delegates to worker agents,\nruns independent read-only reviews and keeps your setup in sync across machines.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       v,
	}
	root.AddCommand(
		installCmd(), applyCmd(), doctorCmd(), syncCmd(),
		modelsCmd(), profileCmd(), reviewCmd(), providersCmd(), askCmd(), skillsCmd(), projectCmd(), vaultCmd(),
		commsCmd(), configCmd(), exportCmd(), importCmd(), notifyCmd(), selfInstallCmd(), uninstallCmd(),
	)
	if err := root.Execute(); err != nil {
		var ec exitCode
		if errors.As(err, &ec) {
			return int(ec) // details were already printed
		}
		fmt.Fprintln(os.Stderr, "conductor:", err)
		return 1
	}
	return 0
}

// exitCode lets a command fail with a specific status.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// selfPath returns a stable path to this binary for hooks, or "" when running
// from a temporary build (go run), where a hook would break later.
func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if strings.Contains(p, string(filepath.Separator)+"go-build") {
		return ""
	}
	return p
}

// apply re-renders Claude files from the current configuration.
func apply(quiet bool) error {
	if err := paths.EnsureDirs(); err != nil {
		return err
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	hook := selfPath()
	changes, err := claude.Apply(c, hook)
	if err != nil {
		return err
	}
	markApplied()
	if quiet {
		return nil
	}
	for _, ch := range changes {
		if ch.Action != "unchanged" {
			ui.Okf("%-9s %s", ch.Action, tilde(ch.Path))
		}
	}
	if hook == "" && c.Sync.Auto {
		ui.Warnf("auto-sync hook not installed: run an installed conductor binary, not `go run`")
	}
	return nil
}

func applyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Regenerate the skill, agents and Claude settings from your configuration",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := apply(false); err != nil {
				return err
			}
			ui.Linef("Applied.")
			return nil
		},
	}
}

// changed saves a config edit and re-renders unless --no-apply.
func changed(cmd *cobra.Command) error {
	if noApply, _ := cmd.Flags().GetBool("no-apply"); noApply {
		ui.Linef("Saved. Run `conductor apply` to update Claude files.")
		return nil
	}
	if err := apply(false); err != nil {
		return err
	}
	ui.Linef("Saved and applied.")
	return nil
}

// editFlags adds the flags every config-changing command shares.
func editFlags(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().Bool("local", false, "store on this machine only (not synced)")
	cmd.Flags().Bool("no-apply", false, "save without regenerating Claude files")
	return cmd
}

func isLocal(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("local")
	return v
}

// tilde shortens a path under the home directory for display.
func tilde(p string) string {
	h := paths.Home()
	if rel, err := filepath.Rel(h, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return p
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
