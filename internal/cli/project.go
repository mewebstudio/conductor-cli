package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/project"
	"github.com/mewebstudio/conductor-cli/internal/review"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

// docsDir is the folder that holds the decision log (linked into the vault).
func docsDir(c *config.Config) string {
	d := filepath.Dir(filepath.FromSlash(c.Docs.Decisions))
	if d == "." {
		d = "docs"
	}
	return d
}

// vaultPath returns the expanded vault folder, or "" when the vault is off.
func vaultPath(c *config.Config) string {
	if !c.Obsidian.Enabled || c.Obsidian.Vault == "" {
		return ""
	}
	p, err := paths.Expand(c.Obsidian.Vault)
	if err != nil {
		return ""
	}
	return p
}

// portable stores paths under home as ~/… so they work on every machine.
func portable(p string) string {
	if rel, err := filepath.Rel(paths.Home(), p); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + filepath.ToSlash(rel)
	}
	return p
}

func projectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Prepare repositories for conductor and list them",
	}
	initC := &cobra.Command{
		Use:   "init [folder]",
		Short: "Create docs/decisions.md, docs/handoff.md and .conductor.conf; link into the vault",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			abs, err := paths.Expand(dir)
			if err != nil {
				return err
			}
			root := review.ProjectRoot(abs)
			if force, _ := cmd.Flags().GetBool("force"); !force {
				if err := checkProjectRoot(root); err != nil {
					return err
				}
			}
			c, err := config.Load()
			if err != nil {
				return err
			}
			name, _ := cmd.Flags().GetString("name")
			if name == "" {
				name = project.NameFor(root)
			}
			made, err := project.Docs(root, c.Docs.Decisions, c.Docs.Handoff)
			if err != nil {
				return err
			}
			for _, f := range made {
				ui.Okf("created %s", f)
			}
			confPath := filepath.Join(root, project.ConfName)
			if _, err := os.Stat(confPath); err != nil {
				conf := project.Detect(root)
				if err := os.WriteFile(confPath, []byte(project.ConfText(conf)), 0o644); err != nil {
					return err
				}
				ui.Okf("created %s (test: %s)", project.ConfName, orDash(conf.Test))
				if strings.Contains(conf.Test, "<Scheme>") {
					ui.Warnf("edit %s: replace <Scheme> with your Xcode scheme", project.ConfName)
				}
			}
			if err := review.EnsureIgnored(root, c.Docs.Reviews); err != nil {
				return err
			}
			p := project.Project{Name: name, Path: root}
			if err := project.Register(p); err != nil {
				return err
			}
			if v := vaultPath(c); v != "" {
				if err := project.Link(v, p, docsDir(c)); err != nil {
					ui.Warnf("vault: %v", err)
				} else {
					ui.Okf("linked into vault as Projects/%s", name)
				}
				if err := project.WriteHome(v, c.Docs.Decisions, c.Docs.Handoff, project.List()); err != nil {
					return err
				}
			}
			ui.Linef("Project %q ready. New files are not committed; review and commit them yourself.", name)
			return nil
		},
	}
	initC.Flags().String("name", "", "name shown in the vault (default: folder name)")
	initC.Flags().Bool("force", false, "skip the home/root folder check")

	list := &cobra.Command{
		Use:   "list",
		Short: "List projects registered on this machine",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			v := vaultPath(c)
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "PROJECT\tPATH\tVAULT")
			for _, p := range project.List() {
				state := "-"
				if v != "" {
					state = string(project.Inspect(v, p, docsDir(c)))
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", p.Name, tilde(p.Path), state)
			}
			return w.Flush()
		},
	}
	forget := &cobra.Command{
		Use:   "forget <name>",
		Short: "Unregister a project and remove its vault link (files stay)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			if v := vaultPath(c); v != "" {
				if err := project.Unlink(v, args[0]); err != nil {
					return err
				}
			}
			if err := project.Unregister(args[0]); err != nil {
				return err
			}
			if v := vaultPath(c); v != "" {
				_ = project.WriteHome(v, c.Docs.Decisions, c.Docs.Handoff, project.List())
			}
			ui.Okf("%s forgotten", args[0])
			return nil
		},
	}
	cmd.AddCommand(initC, list, forget)
	return cmd
}

// checkProjectRoot refuses folders that cannot be a project: the home
// folder, a filesystem root, or any folder above home (e.g. /Users).
// Projects do not need to be git repositories.
func checkProjectRoot(root string) error {
	clean, home := filepath.Clean(root), filepath.Clean(paths.Home())
	switch {
	case clean == home:
		return fmt.Errorf("%s is your home folder, not a project; run this inside a project folder", tilde(clean))
	case clean == filepath.Dir(clean):
		return fmt.Errorf("%s is a filesystem root, not a project", clean)
	case strings.HasPrefix(home, clean+string(filepath.Separator)):
		return fmt.Errorf("%s contains your home folder, not a project", clean)
	}
	return nil
}

func vaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "One Obsidian vault for all your projects' decisions, handoffs and reviews",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			v := vaultPath(c)
			if v == "" {
				ui.Linef("Vault: off. Turn it on with `conductor vault set <folder>`.")
				return nil
			}
			ui.Linef("Vault: %s\n", tilde(v))
			for _, p := range project.List() {
				ui.Linef("  %-24s %s", p.Name, project.Inspect(v, p, docsDir(c)))
			}
			return nil
		},
	}
	set := editFlags(&cobra.Command{
		Use:   "set <folder>",
		Short: "Turn the vault on (or move it) and link every registered project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			abs, err := paths.Expand(args[0])
			if err != nil {
				return err
			}
			if old := currentVault(); old != "" && filepath.Clean(old) != filepath.Clean(abs) {
				for _, p := range project.List() {
					_ = project.Unlink(old, p.Name)
				}
			}
			if err := config.SetValue("obsidian.vault", portable(abs), isLocal(cmd)); err != nil {
				return err
			}
			if err := config.SetValue("obsidian.enabled", true, isLocal(cmd)); err != nil {
				return err
			}
			if err := relinkVault(); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	off := editFlags(&cobra.Command{
		Use:   "off",
		Short: "Turn the vault off and remove project links (your docs stay in each project)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if v := currentVault(); v != "" {
				for _, p := range project.List() {
					if err := project.Unlink(v, p.Name); err != nil {
						ui.Warnf("%v", err)
					}
				}
			}
			if err := config.SetValue("obsidian.enabled", false, isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	link := &cobra.Command{
		Use:   "link",
		Short: "Re-create links for every registered project",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return relinkVault() },
	}
	open := &cobra.Command{
		Use:   "open",
		Short: "Open the vault in Obsidian",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			v := currentVault()
			if v == "" {
				return fmt.Errorf("the vault is off; run `conductor vault set <folder>`")
			}
			return project.OpenVault(v)
		},
	}
	cmd.AddCommand(set, off, link, open)
	return cmd
}

func currentVault() string {
	c, err := config.Load()
	if err != nil {
		return ""
	}
	return vaultPath(c)
}

// relinkVault links every registered project and refreshes Home.md.
func relinkVault() error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	v := vaultPath(c)
	if v == "" {
		return nil
	}
	for _, p := range project.List() {
		if _, err := os.Stat(p.Path); err != nil {
			ui.Warnf("%s: %s no longer exists (`conductor project forget %s`)", p.Name, tilde(p.Path), p.Name)
			continue
		}
		if err := project.Link(v, p, docsDir(c)); err != nil {
			ui.Warnf("%s: %v", p.Name, err)
		} else {
			ui.Okf("%s linked", p.Name)
		}
	}
	return project.WriteHome(v, c.Docs.Decisions, c.Docs.Handoff, project.List())
}
