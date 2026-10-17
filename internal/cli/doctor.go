package cli

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mewebstudio/conductor-cli/internal/claude"
	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/providers"
	"github.com/mewebstudio/conductor-cli/internal/skills"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check requirements, sign-ins, keys and whether generated files are current",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			problems := 0
			fail := func(f string, a ...any) { problems++; ui.Failf(f, a...) }

			ui.Linef("Tools")
			for _, t := range []struct{ bin, why string }{
				{"claude", "Claude Code"},
				{"git", "settings sync and reviews"},
			} {
				if p, err := exec.LookPath(t.bin); err == nil {
					ui.Okf("%s (%s)", t.bin, tilde(p))
				} else {
					fail("%s not found on PATH (%s)", t.bin, t.why)
				}
			}
			if self := selfPath(); self == "" {
				ui.Warnf("running a temporary build; install the binary so hooks keep working")
			} else if p, err := exec.LookPath("conductor"); err != nil {
				ui.Warnf("conductor is not on PATH (this binary: %s)", tilde(self))
			} else {
				ui.Okf("conductor on PATH (%s)", tilde(p))
			}

			ui.Linef("\nSettings")
			c, err := config.Load()
			if err != nil {
				fail("cannot load settings: %v", err)
				return exitCode(1)
			}
			if config.Exists() {
				ui.Okf("settings file %s", tilde(paths.ConfigFile()))
			} else {
				ui.Warnf("no settings file yet; defaults in use (run `conductor install`)")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			switch {
			case !configIsRepo():
				ui.Warnf("settings are not in a git repository; they will not sync between machines")
			case configRemote(ctx) == "":
				ui.Warnf("settings repository has no remote")
			default:
				ui.Okf("settings repository → %s", configRemote(ctx))
				if st, _ := cfgGit(ctx, "status", "--porcelain"); strings.TrimSpace(st) != "" {
					ui.Warnf("unsynced local settings changes; run `conductor sync`")
				}
			}

			ui.Linef("\nClaude Code files")
			status, err := claude.Status(c)
			if err != nil {
				fail("%v", err)
			}
			for _, s := range status {
				switch s.Action {
				case "ok":
					ui.Okf("%s", tilde(s.Path))
				case "edited-by-hand":
					ui.Warnf("%s was edited by hand; `conductor apply` will overwrite it (a backup is kept)", tilde(s.Path))
				default:
					fail("%s is %s; run `conductor apply`", tilde(s.Path), s.Action)
				}
			}
			model, effort, hook, err := claude.SettingsState()
			switch {
			case err != nil:
				fail("settings.json: %v", err)
			case model != c.Models.Lead.Model || effort != c.Models.Lead.Effort:
				fail("settings.json lead is %s/%s, expected %s/%s; run `conductor apply`", orDash(model), orDash(effort), c.Models.Lead.Model, orDash(c.Models.Lead.Effort))
			default:
				ui.Okf("lead session: %s · %s", model, orDash(effort))
			}
			if c.Sync.Auto && !hook {
				ui.Warnf("auto-sync hook missing; run `conductor apply` from the installed binary")
			}

			ui.Linef("\nProviders")
			for _, p := range providers.All() {
				if !c.ProviderEnabled(p.Name()) {
					ui.Linef("  · %s (off)", p.Name())
					continue
				}
				if err := p.Check(); err != nil {
					fail("%s: %v", p.Name(), err)
				} else {
					ui.Okf("%s", p.Name())
				}
			}
			for dir, err := range providers.PluginProblems() {
				fail("plugin %s: %v", tilde(dir), err)
			}

			ui.Linef("\nSkills")
			if sel, loc, err := skills.Combined(); err != nil {
				fail("cannot read skills list: %v", err)
			} else {
				targets := skills.Targets(sel)
				names := make([]string, len(targets))
				for i, a := range targets {
					names[i] = a.Key
				}
				ui.Okf("agents: %s", orDash(strings.Join(names, ", ")))
				bad := 0
				for _, k := range append(append([]skills.Skill{}, sel.Skills...), loc.Skills...) {
					if st := skillState(skills.Inspect(k.Name, targets)); st != "ok" && !strings.HasPrefix(st, "ok ") {
						bad++
						ui.Warnf("%s: %s", k.Name, st)
					}
				}
				ui.Okf("%d skill(s), %d plugin(s) in your list; %d need attention", len(sel.Skills)+len(loc.Skills), len(sel.Plugins), bad)
				if bad > 0 || skills.Pending() {
					ui.Warnf("run `conductor skills sync`")
				}
			}

			if problems > 0 {
				ui.Linef("\n%d problem(s) found.", problems)
				return exitCode(1)
			}
			ui.Linef("\nAll good.")
			return nil
		},
	}
}
