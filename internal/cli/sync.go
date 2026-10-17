package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// cfgGit runs git inside the config repository.
func cfgGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = paths.ConfigDir()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(msg)
	}
	return out.String(), nil
}

func configIsRepo() bool {
	_, err := os.Stat(filepath.Join(paths.ConfigDir(), ".git"))
	return err == nil
}

func configRemote(ctx context.Context) string {
	out, err := cfgGit(ctx, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func hasUpstream(ctx context.Context) bool {
	_, err := cfgGit(ctx, "rev-parse", "--abbrev-ref", "@{u}")
	return err == nil
}

// setupConfigRepo prepares the settings folder: it clones url when given
// (an empty remote is fine), turns an existing folder into a repository with
// that remote, or — with no url — makes the default folder a local repository
// and leaves a custom folder (iCloud, Dropbox…) as a plain folder.
func setupConfigRepo(ctx context.Context, url string) error {
	if url == "" && !configIsRepo() && paths.ConfigDir() != paths.DefaultConfigDir() {
		return os.MkdirAll(paths.ConfigDir(), 0o755)
	}
	if configIsRepo() {
		if url != "" && configRemote(ctx) == "" {
			_, err := cfgGit(ctx, "remote", "add", "origin", url)
			return err
		}
		return nil
	}
	entries, _ := os.ReadDir(paths.ConfigDir())
	if url != "" && len(entries) == 0 {
		_ = os.RemoveAll(paths.ConfigDir())
		cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", url, paths.ConfigDir())
		var errb bytes.Buffer
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("cloning %s: %s", url, strings.TrimSpace(errb.String()))
		}
		ui.Okf("cloned settings repository into %s", tilde(paths.ConfigDir()))
		return nil
	}
	if err := os.MkdirAll(paths.ConfigDir(), 0o755); err != nil {
		return err
	}
	if _, err := cfgGit(ctx, "init", "--quiet", "-b", "main"); err != nil {
		return err
	}
	if url != "" {
		if _, err := cfgGit(ctx, "remote", "add", "origin", url); err != nil {
			return err
		}
	}
	return nil
}

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Pull settings from your config repo, push local edits, and re-apply",
		Long: "Pulls ~/.conductor-cli/config from its remote, commits and pushes settings you\n" +
			"changed on this machine, then regenerates Claude files.\n" +
			"--hook (used at Claude session start) only pulls and applies, silently.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hook, _ := cmd.Flags().GetBool("hook")
			if hook {
				runHookSync()
				return nil
			}
			noPush, _ := cmd.Flags().GetBool("no-push")
			msg, _ := cmd.Flags().GetString("message")
			return runSync(cmd.Context(), !noPush, msg)
		},
	}
	cmd.Flags().Bool("hook", false, "quiet pull-and-apply for the session-start hook")
	cmd.Flags().Bool("no-push", false, "pull and apply, but do not commit or push local edits")
	cmd.Flags().StringP("message", "m", "Update conductor settings", "commit message for local edits")
	return cmd
}

// runHookSync never fails and never prints: hook output would land in the
// Claude session's context. Missing skills install in a detached background
// process so the session never waits.
func runHookSync() {
	c, err := config.Load()
	if err != nil || !c.Sync.Auto {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if configIsRepo() && configRemote(ctx) != "" && hasUpstream(ctx) {
		_, _ = cfgGit(ctx, "pull", "--ff-only", "--quiet")
	}
	if settingsChanged() {
		_ = apply(true)
	}
	if skills.Pending() {
		startBackgroundSkillSync()
	}
}

// settingsChanged compares the settings files with the last applied version.
func settingsChanged() bool {
	h := settingsHash()
	stamp := filepath.Join(paths.StateDir(), "applied-settings")
	old, _ := os.ReadFile(stamp)
	return strings.TrimSpace(string(old)) != h
}

func settingsHash() string {
	var all []byte
	for _, f := range []string{paths.ConfigFile(), paths.LocalFile(), paths.SkillsFile()} {
		b, _ := os.ReadFile(f)
		all = append(append(all, b...), 0)
	}
	return fsutil.Hash(all)
}

func markApplied() {
	_ = os.MkdirAll(paths.StateDir(), 0o755)
	_ = os.WriteFile(filepath.Join(paths.StateDir(), "applied-settings"), []byte(settingsHash()+"\n"), 0o644)
}

func startBackgroundSkillSync() {
	self := selfPath()
	if self == "" {
		return
	}
	logf, err := os.OpenFile(filepath.Join(paths.StateDir(), "skills-sync.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	cmd := exec.Command(self, "skills", "sync")
	cmd.Stdout, cmd.Stderr = logf, logf
	detach(cmd)
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
	logf.Close()
}

func runSync(ctx context.Context, push bool, msg string) error {
	if configIsRepo() {
		if err := gitSync(ctx, push, msg); err != nil {
			return err
		}
	} else {
		ui.Linef("Settings folder %s is not a git repository; syncing it is left to your file-sync service.", tilde(paths.ConfigDir()))
	}
	if err := apply(false); err != nil {
		return err
	}
	if err := relinkVault(); err != nil {
		ui.Warnf("vault: %v", err)
	}
	if err := runSkillSync(ctx, false); err != nil {
		return err
	}
	ui.Linef("In sync.")
	return nil
}

func gitSync(ctx context.Context, push bool, msg string) error {
	remote := configRemote(ctx)
	if remote != "" && hasUpstream(ctx) {
		if _, err := cfgGit(ctx, "pull", "--ff-only", "--quiet"); err != nil {
			return fmt.Errorf("pull failed (resolve in %s): %w", tilde(paths.ConfigDir()), err)
		}
		ui.Okf("pulled from %s", remote)
	}
	if !push {
		return nil
	}
	status, err := cfgGit(ctx, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		ui.Linef("Local settings changes:\n%s", status)
		if _, err := cfgGit(ctx, "add", "-A"); err != nil {
			return err
		}
		if _, err := cfgGit(ctx, "commit", "--quiet", "-m", msg); err != nil {
			return err
		}
		ui.Okf("committed settings")
	}
	if remote == "" {
		ui.Warnf("no remote set; add one with: git -C %s remote add origin <url>", tilde(paths.ConfigDir()))
		return nil
	}
	ahead := "1"
	if hasUpstream(ctx) {
		out, _ := cfgGit(ctx, "rev-list", "--count", "@{u}..HEAD")
		ahead = strings.TrimSpace(out)
	}
	if _, err := cfgGit(ctx, "rev-parse", "HEAD"); err != nil {
		return nil // nothing committed yet
	}
	if ahead != "0" {
		if _, err := cfgGit(ctx, "push", "--quiet", "-u", "origin", "HEAD"); err != nil {
			return fmt.Errorf("push failed: %w", err)
		}
		ui.Okf("pushed to %s", remote)
	}
	return nil
}
