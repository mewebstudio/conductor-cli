package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mewebstudio/conductor-cli/internal/agents"
	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/skills"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

func skillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Find, add and sync skills for every coding agent (and Claude plugins)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			sel, loc, err := skills.Combined()
			if err != nil {
				return err
			}
			targets := skills.Targets(sel)
			names := make([]string, len(targets))
			for i, a := range targets {
				names[i] = a.Name
			}
			ui.Linef("Agents: %s\n", orDash(strings.Join(names, ", ")))
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "SKILL\tSOURCE\tVERSION\tSTATE")
			list := append(append([]skills.Skill{}, sel.Skills...), loc.Skills...)
			for _, k := range list {
				st := skills.Inspect(k.Name, targets)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", k.Name, sourceLabel(k), orDash(shortRef(k.Ref)), skillState(st))
			}
			installed := skills.InstalledPlugins()
			for _, p := range sel.Plugins {
				state := "installed"
				if !installed[p.ID] {
					state = "missing"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, "Claude plugin", "-", state)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if len(list)+len(sel.Plugins) == 0 {
				ui.Linef("No skills yet. Try `conductor skills search <topic>` or `conductor skills add <owner/repo>`.")
			}
			ex := skills.FindExisting(sel)
			if n := len(ex.Skills) + len(ex.Unknown) + len(ex.Plugins); n > 0 {
				ui.Linef("\n%d skill(s)/plugin(s) on this machine are not in your list; run `conductor skills import`.", n)
			}
			return nil
		},
	}

	search := &cobra.Command{
		Use:   "search <query>",
		Short: "Search skills.sh, skills on this machine and Claude plugin marketplaces",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			noPlugins, _ := cmd.Flags().GetBool("no-plugins")
			return searchAndAdd(cmd.Context(), strings.Join(args, " "), !noPlugins)
		},
	}
	search.Flags().Bool("no-plugins", false, "skip Claude plugin marketplaces")

	add := &cobra.Command{
		Use:   "add <name|owner/repo[/path]|git-url|plugin@marketplace|folder>...",
		Short: "Add skills or Claude plugins to your list and install them",
		Example: "  conductor skills add swiftui-expert-skill\n" +
			"  conductor skills add avdlee/swiftui-agent-skill\n" +
			"  conductor skills add https://github.com/owner/repo/tree/main/skills/foo\n" +
			"  conductor skills add frontend-design@claude-plugins-official\n" +
			"  conductor skills add ./my-skill",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			all, _ := cmd.Flags().GetBool("all")
			return addSkills(cmd.Context(), args, all)
		},
	}
	add.Flags().Bool("all", false, "when a repository holds several skills, add all of them")

	remove := &cobra.Command{
		Use:   "remove <name|plugin@marketplace>...",
		Short: "Remove skills or plugins from your list and uninstall them",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, loc, err := skills.Combined()
			if err != nil {
				return err
			}
			for _, name := range args {
				a, b := sel.Drop(name), loc.Drop(name)
				if !a && !b {
					ui.Warnf("%s is not in your list", name)
				}
			}
			if err := sel.Save(); err != nil {
				return err
			}
			if err := loc.SaveLocal(); err != nil {
				return err
			}
			return runSkillSync(cmd.Context(), false)
		},
	}

	update := &cobra.Command{
		Use:   "update [name]...",
		Short: "Check for newer versions; shows what changed and asks before updating",
		RunE: func(cmd *cobra.Command, args []string) error {
			yes, _ := cmd.Flags().GetBool("yes")
			return updateSkills(cmd.Context(), args, yes)
		},
	}
	update.Flags().Bool("yes", false, "update without asking")

	syncC := &cobra.Command{
		Use:   "sync",
		Short: "Install what your list has and this machine lacks; remove what you dropped",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			quiet, _ := cmd.Flags().GetBool("quiet")
			return runSkillSync(cmd.Context(), quiet)
		},
	}
	syncC.Flags().Bool("quiet", false, "print nothing (used in the background)")

	imp := &cobra.Command{
		Use:   "import",
		Short: "Add skills and plugins already on this machine to your list",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			yes, _ := cmd.Flags().GetBool("yes")
			return importExisting(yes)
		},
	}
	imp.Flags().Bool("yes", false, "import everything without asking")

	adopt := &cobra.Command{
		Use:   "adopt <name> <owner/repo[/path]|git-url>",
		Short: "Take over a skill that is already installed, recording where it comes from",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, sub, ok := skills.ParseRepo(args[1])
			if !ok {
				return fmt.Errorf("%q is not a repository", args[1])
			}
			k := skills.Skill{Name: args[0], Source: repoSource(args[1]), Path: sub}
			if err := skills.Adopt(k, ""); err != nil {
				return err
			}
			sel, err := skills.Load()
			if err != nil {
				return err
			}
			sel.Put(k)
			if err := sel.Save(); err != nil {
				return err
			}
			ui.Okf("%s adopted; `conductor skills update %s` pins it to the latest version", k.Name, k.Name)
			return runSkillSync(cmd.Context(), false)
		},
	}

	cmd.AddCommand(search, add, remove, update, syncC, imp, adopt, skillAgentsCmd())
	return cmd
}

func trim(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func shortRef(r string) string {
	if len(r) > 7 {
		return r[:7]
	}
	return r
}

func sourceLabel(k skills.Skill) string {
	if skills.IsLocalSource(k.Source) {
		return tilde(k.Source) + " (this machine)"
	}
	if k.Path != "" {
		return k.Source + "/" + k.Path
	}
	return k.Source
}

func skillState(st skills.State) string {
	if !st.Installed {
		return "missing"
	}
	if !st.Managed {
		return "installed (not managed; `skills adopt`)"
	}
	var missing, foreign []string
	for agent, l := range st.Links {
		switch l {
		case agents.Missing:
			missing = append(missing, agent)
		case agents.Foreign:
			foreign = append(foreign, agent)
		}
	}
	switch {
	case len(missing) > 0:
		slices.Sort(missing)
		return "not linked: " + strings.Join(missing, ",")
	case len(foreign) > 0:
		slices.Sort(foreign)
		return "ok (own copy in " + strings.Join(foreign, ",") + ")"
	}
	return "ok"
}

// repoSource normalizes a repo argument for storage (owner/repo or URL, no sub-path).
func repoSource(arg string) string {
	if skills.IsRemote(arg) {
		u, _, _ := skills.ParseRepo(arg)
		return strings.TrimSuffix(u, ".git")
	}
	parts := strings.SplitN(arg, "/", 3)
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
}

// addSkills resolves each argument (an optional "#name" suffix picks one skill
// from a repository), installs, and records the choices.
func addSkills(ctx context.Context, args []string, all bool) error {
	sel, loc, err := skills.Combined()
	if err != nil {
		return err
	}
	targets := skills.Targets(sel)
	failed := 0
	for _, raw := range args {
		arg, want, _ := strings.Cut(raw, "#")
		var picks []skills.Skill
		switch {
		case skills.IsPluginID(arg):
			_, mk, _ := strings.Cut(arg, "@")
			p := skills.Plugin{ID: arg, Marketplace: skills.MarketplaceSource(mk)}
			if err := skills.InstallPlugin(ctx, p); err != nil {
				ui.Failf("%s: %v", arg, err)
				failed++
				continue
			}
			_ = skills.MarkPluginOwned(arg)
			sel.PutPlugin(p)
			ui.Okf("%s installed (Claude plugin)", arg)
			continue
		case isDir(arg):
			dir, _ := paths.Expand(arg)
			name, _ := skills.Frontmatter(filepath.Join(dir, "SKILL.md"))
			if name == "" {
				name = filepath.Base(dir)
			}
			k := skills.Skill{Name: name, Source: dir}
			if _, _, err := skills.Install(ctx, k, targets, false); err != nil {
				ui.Failf("%s: %v", arg, err)
				failed++
				continue
			}
			loc.Put(k)
			ui.Okf("%s installed from %s (this machine only)", name, tilde(dir))
			continue
		}
		if _, _, ok := skills.ParseRepo(arg); !ok {
			resolved, err := pickFromRegistry(ctx, arg)
			if err != nil {
				ui.Failf("%s: %v", arg, err)
				failed++
				continue
			}
			arg, want = resolved.Source, resolved.Name
		}
		picks, err = pickFromRepo(ctx, arg, want, all)
		if err != nil {
			ui.Failf("%s: %v", arg, err)
			failed++
			continue
		}
		for _, k := range picks {
			commit, problems, err := skills.Install(ctx, k, targets, false)
			if errors.Is(err, skills.ErrForeign) {
				ui.Warnf("%s is already installed by another tool; taking it over", k.Name)
				if err = skills.Adopt(k, ""); err == nil {
					problems = skills.LinkAll(k.Name, targets)
				}
			}
			if err != nil {
				ui.Failf("%s: %v", k.Name, err)
				failed++
				continue
			}
			k.Ref = commit
			sel.Put(k)
			ui.Okf("%s installed (%s@%s) for %d agent(s)", k.Name, k.Source, shortRef(commit), len(targets))
			for agent, perr := range problems {
				ui.Warnf("  %s: %v", agent, perr)
			}
		}
	}
	if err := sel.Save(); err != nil {
		return err
	}
	if err := loc.SaveLocal(); err != nil {
		return err
	}
	if failed > 0 {
		return exitCode(1)
	}
	return nil
}

func isDir(p string) bool {
	if !(strings.HasPrefix(p, ".") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || filepath.IsAbs(p)) {
		return false
	}
	abs, err := paths.Expand(p)
	if err != nil {
		return false
	}
	fi, err := os.Stat(abs)
	return err == nil && fi.IsDir()
}

// pickFromRegistry turns a bare name into a registry hit.
func pickFromRegistry(ctx context.Context, name string) (skills.Result, error) {
	hits, err := skills.SearchRegistry(ctx, name)
	if err != nil {
		return skills.Result{}, fmt.Errorf("searching skills.sh: %w", err)
	}
	if len(hits) == 0 {
		return skills.Result{}, errors.New("no skill with that name on skills.sh; give owner/repo instead")
	}
	var exact []skills.Result
	for _, h := range hits {
		if h.Name == name {
			exact = append(exact, h)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if !ui.Interactive() {
		list := hits
		if len(exact) > 1 {
			list = exact
		}
		var srcs []string
		for i, h := range list {
			if i == 5 {
				break
			}
			srcs = append(srcs, h.Source+"#"+h.Name)
		}
		return skills.Result{}, fmt.Errorf("several matches; pick one: %s", strings.Join(srcs, ", "))
	}
	cands := hits
	if len(exact) > 1 {
		cands = exact
	}
	if len(cands) > 15 {
		cands = cands[:15]
	}
	opts := make([]ui.Option, len(cands))
	for i, h := range cands {
		opts[i] = ui.Option{Label: fmt.Sprintf("%-32s %s  (%d installs)", trim(h.Name, 32), h.Source, h.Installs), Value: fmt.Sprint(i)}
	}
	v, err := ui.Select("Which "+name+"?", "From skills.sh; sources are third-party code.", opts, "0")
	if err != nil {
		return skills.Result{}, err
	}
	var i int
	fmt.Sscan(v, &i)
	return cands[i], nil
}

// pickFromRepo fetches a repository and chooses skills from it.
func pickFromRepo(ctx context.Context, arg, want string, all bool) ([]skills.Skill, error) {
	url, sub, _ := skills.ParseRepo(arg)
	dir, commit, err := skills.Fetch(ctx, url, "")
	if err != nil {
		return nil, err
	}
	found, err := skills.Discover(dir)
	if err != nil {
		return nil, err
	}
	var cands []skills.Found
	for _, f := range found {
		switch {
		case want != "":
			if f.Name == want || filepath.Base(f.Path) == want {
				cands = append(cands, f)
			}
		case sub != "":
			if f.Path == sub || strings.HasPrefix(f.Path, sub+"/") {
				cands = append(cands, f)
			}
		default:
			cands = append(cands, f)
		}
	}
	if len(cands) == 0 {
		return nil, errors.New("no SKILL.md found there")
	}
	src := repoSource(arg)
	mk := func(f skills.Found) skills.Skill {
		return skills.Skill{Name: f.Name, Source: src, Path: f.Path, Ref: commit}
	}
	if len(cands) == 1 || all {
		out := make([]skills.Skill, len(cands))
		for i, f := range cands {
			out[i] = mk(f)
		}
		return out, nil
	}
	if !ui.Interactive() {
		names := make([]string, len(cands))
		for i, f := range cands {
			names[i] = f.Name
		}
		return nil, fmt.Errorf("%d skills in this repository (%s); add one with %s#<name> or use --all", len(cands), strings.Join(names, ", "), arg)
	}
	opts := make([]ui.Option, len(cands))
	for i, f := range cands {
		opts[i] = ui.Option{Label: fmt.Sprintf("%-32s %s", trim(f.Name, 32), trim(f.Description, 70)), Value: fmt.Sprint(i)}
	}
	picked, err := ui.MultiSelect(fmt.Sprintf("%d skills in %s", len(cands), src), "Space to select, Enter to add.", opts, nil)
	if err != nil {
		return nil, err
	}
	var out []skills.Skill
	for _, p := range picked {
		var i int
		fmt.Sscan(p, &i)
		out = append(out, mk(cands[i]))
	}
	return out, nil
}

func runSkillSync(ctx context.Context, quiet bool) error {
	failed := 0
	err := skills.Sync(ctx, func(e skills.Event) {
		if e.Action == "failed" {
			failed++
		}
		if quiet {
			return
		}
		switch e.Action {
		case "failed":
			ui.Failf("%s: %s", e.Name, e.Detail)
		case "skipped":
			ui.Warnf("%s: %s", e.Name, e.Detail)
		default:
			msg := e.Name + " " + e.Action
			if e.Detail != "" {
				msg += " (" + e.Detail + ")"
			}
			ui.Okf("%s", msg)
		}
	})
	if err != nil {
		return err
	}
	if failed > 0 {
		return exitCode(1)
	}
	return nil
}

func updateSkills(ctx context.Context, names []string, yes bool) error {
	sel, err := skills.Load()
	if err != nil {
		return err
	}
	changedAny := false
	for i, k := range sel.Skills {
		if len(names) > 0 && !slices.Contains(names, k.Name) {
			continue
		}
		if skills.IsLocalSource(k.Source) {
			continue
		}
		dir, latest, err := skills.Fetch(ctx, skills.CloneURL(k.Source), "")
		if err != nil {
			ui.Failf("%s: %v", k.Name, err)
			continue
		}
		if latest == k.Ref {
			ui.Okf("%s is up to date", k.Name)
			continue
		}
		summary := skills.ChangeSummary(ctx, dir, k.Ref, latest, k.Path)
		ui.Linef("%s: %s → %s\n%s", k.Name, orDash(shortRef(k.Ref)), shortRef(latest), summary)
		ok := yes
		if !yes {
			if !ui.Interactive() {
				ui.Warnf("run again with --yes to update %s", k.Name)
				continue
			}
			if ok, err = ui.Confirm("Update "+k.Name+"?", "", true); err != nil {
				return err
			}
		}
		if ok {
			sel.Skills[i].Ref = latest
			changedAny = true
		}
	}
	if !changedAny {
		return nil
	}
	if err := sel.Save(); err != nil {
		return err
	}
	return runSkillSync(ctx, false)
}

func importExisting(yes bool) error {
	sel, err := skills.Load()
	if err != nil {
		return err
	}
	ex := skills.FindExisting(sel)
	if len(ex.Skills)+len(ex.Plugins) == 0 {
		ui.Linef("Nothing new to import.")
	}
	var opts []ui.Option
	var defs []string
	for i, k := range ex.Skills {
		v := fmt.Sprintf("s%d", i)
		opts = append(opts, ui.Option{Label: fmt.Sprintf("skill   %-32s %s", trim(k.Name, 32), sourceLabel(k)), Value: v})
		defs = append(defs, v)
	}
	for i, p := range ex.Plugins {
		v := fmt.Sprintf("p%d", i)
		opts = append(opts, ui.Option{Label: fmt.Sprintf("plugin  %s", p.ID), Value: v})
		defs = append(defs, v)
	}
	picked := defs
	if len(opts) > 0 && !yes {
		if !ui.Interactive() {
			return errors.New("run in a terminal, or pass --yes to import everything")
		}
		if picked, err = ui.MultiSelect("Add these to your list?", "They stay where they are; conductor records their source so other machines get them too.", opts, defs); err != nil {
			return err
		}
	}
	for _, v := range picked {
		var i int
		fmt.Sscan(v[1:], &i)
		if v[0] == 's' {
			k := ex.Skills[i]
			if _, err := os.Stat(filepath.Join(skills.SharedPath(k.Name), "SKILL.md")); err == nil {
				if err := skills.Adopt(k, ""); err != nil {
					ui.Failf("%s: %v", k.Name, err)
					continue
				}
			} // Claude-only copies stay put; sync adds the shared copy for other agents.
			sel.Put(k)
			ui.Okf("skill %s", k.Name)
		} else {
			p := ex.Plugins[i]
			sel.PutPlugin(p)
			ui.Okf("plugin %s", p.ID)
		}
	}
	if err := sel.Save(); err != nil {
		return err
	}
	for _, n := range ex.Unknown {
		ui.Warnf("%s: source unknown; add it with `conductor skills adopt %s <owner/repo>`", n, n)
	}
	for _, n := range ex.Untracked {
		ui.Linef("  · %s: only in Claude's folder, source unknown; left as is (`conductor skills adopt` after adding it to %s)", n, tilde(agents.SharedDir()))
	}
	return nil
}

func skillAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Show or choose which coding agents receive your skills",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			sel, err := skills.Load()
			if err != nil {
				return err
			}
			chosen := sel.Agents
			if len(chosen) == 0 {
				chosen = agents.Detected(sel.Custom)
				ui.Linef("(no choice saved yet; using the agents found on this machine)\n")
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "AGENT\tKEY\tSKILLS FOLDER\tHERE\tCHOSEN")
			for _, a := range agents.All(sel.Custom) {
				here, pick := a.Installed(), slices.Contains(chosen, a.Key)
				if !here && !pick {
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%v\n", a.Name, a.Key, tilde(a.SkillsDir()), here, pick)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			ui.Linef("\n%d agents are known. Change the choice with `skills agents edit` or `skills agents set <key>...`.", len(agents.All(sel.Custom)))
			return nil
		},
	}
	set := &cobra.Command{
		Use:   "set <key>...",
		Short: "Choose agents by key (see `conductor skills agents`)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := skills.Load()
			if err != nil {
				return err
			}
			for _, k := range args {
				if _, ok := agents.Find(k, sel.Custom); !ok {
					return fmt.Errorf("unknown agent %q", k)
				}
			}
			sel.Agents = args
			if err := sel.Save(); err != nil {
				return err
			}
			return runSkillSync(cmd.Context(), false)
		},
	}
	edit := &cobra.Command{
		Use:   "edit",
		Short: "Choose agents from a menu (agents found here are listed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := chooseAgents(); err != nil {
				return err
			}
			return runSkillSync(cmd.Context(), false)
		},
	}
	addA := &cobra.Command{
		Use:   "add <key> <skills-folder>",
		Short: "Teach conductor about an agent it does not know",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := skills.Load()
			if err != nil {
				return err
			}
			dir, err := paths.Expand(args[1])
			if err != nil {
				return err
			}
			if sel.Custom == nil {
				sel.Custom = map[string]string{}
			}
			sel.Custom[args[0]] = dir
			if !slices.Contains(sel.Agents, args[0]) {
				sel.Agents = append(sel.Agents, args[0])
			}
			if err := sel.Save(); err != nil {
				return err
			}
			return runSkillSync(cmd.Context(), false)
		},
	}
	cmd.AddCommand(set, edit, addA)
	return cmd
}

// chooseAgents asks which installed agents receive skills.
func chooseAgents() error {
	sel, err := skills.Load()
	if err != nil {
		return err
	}
	defs := sel.Agents
	if len(defs) == 0 {
		defs = agents.Detected(sel.Custom)
	}
	var opts []ui.Option
	for _, a := range agents.All(sel.Custom) {
		if !a.Installed() && !slices.Contains(defs, a.Key) {
			continue
		}
		label := fmt.Sprintf("%-18s %s", a.Name, tilde(a.SkillsDir()))
		if a.Universal() {
			label += "  (shared folder)"
		}
		opts = append(opts, ui.Option{Label: label, Value: a.Key})
	}
	if len(opts) == 0 {
		ui.Warnf("no coding agents found on this machine")
		return nil
	}
	picked, err := ui.MultiSelect("Which agents should get your skills?", "Agents found on this machine. Others are added automatically once installed and chosen.", opts, defs)
	if err != nil {
		return err
	}
	sel.Agents = picked
	return sel.Save()
}

// searchAndAdd searches every source and, in a terminal, installs what you pick.
func searchAndAdd(ctx context.Context, query string, withPlugins bool) error {
	results, rerr := skills.Search(ctx, query, withPlugins)
	if rerr != nil {
		ui.Warnf("skills.sh search unavailable: %v", rerr)
	}
	if len(results) == 0 {
		ui.Linef("Nothing found.")
		return nil
	}
	if len(results) > 40 {
		results = results[:40]
	}
	if !ui.Interactive() {
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "KIND\tNAME\tSOURCE\tINSTALLS\tFROM")
		for _, r := range results {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", r.Kind, r.Name, orDash(r.Source), r.Installs, r.Origin)
		}
		return w.Flush()
	}
	var opts []ui.Option
	for i, r := range results {
		if r.Installed {
			continue
		}
		label := fmt.Sprintf("%-7s %-32s %s", r.Kind, trim(r.Name, 32), r.Source)
		if r.Installs > 0 {
			label += fmt.Sprintf("  (%d installs)", r.Installs)
		}
		opts = append(opts, ui.Option{Label: label, Value: fmt.Sprint(i)})
	}
	if len(opts) == 0 {
		ui.Linef("Everything found is already installed.")
		return nil
	}
	picked, err := ui.MultiSelect("Add which?", "Space to select, Enter to add. Sources are third-party code: check them.", opts, nil)
	if err != nil {
		return err
	}
	var args2 []string
	for _, p := range picked {
		var i int
		fmt.Sscan(p, &i)
		r := results[i]
		switch {
		case r.Kind == skills.KindPlugin && skills.IsPluginID(r.Source):
			args2 = append(args2, r.Source)
		case r.Kind == skills.KindPlugin:
			ui.Warnf("%s is listed in Claude's directory; add its marketplace first: claude plugin marketplace add %s", r.Name, r.Source)
		default:
			args2 = append(args2, r.Source+"#"+r.Name)
		}
	}
	return addSkills(ctx, args2, false)
}
