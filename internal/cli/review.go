package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/models"
	"github.com/mewebstudio/conductor-cli/internal/providers"
	"github.com/mewebstudio/conductor-cli/internal/review"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

var reviewPolicies = []string{"milestones", "manual", "every-commit"}

func reviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Show or change the review chain; run a review",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			ui.Linef("Chain:      %s", strings.Join(c.Review.Chain, " → "))
			ui.Linef("Policy:     %s", c.Review.Policy)
			ui.Linef("Fix rounds: %d\n", c.Review.FixRounds)
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "REVIEWER\tMODEL\tEFFORT\tTODAY\tSTATE")
			for _, name := range c.Review.Chain {
				if name == "claude" {
					r := c.Models.Reviewer
					fmt.Fprintf(w, "claude\t%s\t%s\t-\t%s\n", r.Model, orDash(r.Effort), map[bool]string{true: "ready (reviewer agent)", false: "disabled"}[r.Enabled])
					continue
				}
				rv := c.Reviewer(name)
				state := "ready"
				if p, err := providers.Get(name); err != nil {
					state = err.Error()
				} else if err := providers.Ready(c, p); err != nil {
					state = err.Error()
				}
				limit := "∞"
				if rv.DailyLimit > 0 {
					limit = strconv.Itoa(rv.DailyLimit)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d/%s\t%s\n", name, orDash(rv.Model), orDash(rv.Effort), review.UsageToday(name), limit, state)
			}
			return w.Flush()
		},
	}

	run := &cobra.Command{
		Use:   "run --prompt <brief-file> [--base <ref>]",
		Short: "Run the review chain on the current changes (used by the lead)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			brief, _ := cmd.Flags().GetString("prompt")
			base, _ := cmd.Flags().GetString("base")
			chain, _ := cmd.Flags().GetStringSlice("chain")
			wd, _ := os.Getwd()
			res, err := review.Run(cmd.Context(), c, review.Options{
				Root: review.ProjectRoot(wd), BriefFile: brief, Base: base, Chain: chain,
			})
			if err != nil {
				return err
			}
			for _, a := range res.Attempts {
				line := fmt.Sprintf("attempt provider=%s status=%s", a.Provider, a.Status)
				if a.Detail != "" {
					line += " detail=" + strconv.Quote(a.Detail)
				}
				fmt.Println(line)
			}
			switch res.Status {
			case "ok":
				fmt.Printf("status=ok provider=%s output=%s\n", res.Provider, res.Output)
			case "fallback":
				fmt.Printf("status=fallback provider=claude prompt=%s\n", res.Prompt)
			default:
				fmt.Println("status=failed")
				return exitCode(4)
			}
			return nil
		},
	}
	run.Flags().String("prompt", "", "file with the lead's review brief (required)")
	run.Flags().String("base", "HEAD", "git ref to diff against; the working tree is included")
	run.Flags().StringSlice("chain", nil, "override the chain for this run, e.g. --chain nvidia,claude")
	_ = run.MarkFlagRequired("prompt")

	chain := editFlags(&cobra.Command{
		Use:     "chain <reviewer>...",
		Short:   "Set the order reviewers are tried in (claude = read-only reviewer agent)",
		Example: "  conductor review chain codex nvidia claude",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, a := range args {
				if a == "claude" {
					continue
				}
				if _, err := providers.Get(a); err != nil {
					return err
				}
			}
			if err := config.SetValue("review.chain", args, isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})

	set := editFlags(&cobra.Command{
		Use:     "set <reviewer> <model> [effort]",
		Short:   "Set a reviewer's model and effort",
		Example: "  conductor review set codex gpt-6.1-sol medium\n  conductor review set claude claude-opus-5-5 high",
		Args:    cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			effort := ""
			if len(args) == 3 {
				effort = args[2]
			}
			if args[0] == "claude" {
				force, _ := cmd.Flags().GetBool("force")
				if err := setRole("reviewer", args[1], effort, force, isLocal(cmd)); err != nil {
					return err
				}
				return changed(cmd)
			}
			if _, err := providers.Get(args[0]); err != nil {
				return err
			}
			if err := validateReviewer(cmd, args[0], args[1], effort); err != nil {
				return err
			}
			key := "reviewers." + args[0]
			if err := config.SetValue(key+".model", args[1], isLocal(cmd)); err != nil {
				return err
			}
			if err := config.SetValue(key+".effort", effort, isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	set.Flags().Bool("force", false, "accept a model id the tool does not list")

	policy := editFlags(&cobra.Command{
		Use:   "policy <milestones|manual|every-commit>",
		Short: "Set when the lead runs reviews",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !slices.Contains(reviewPolicies, args[0]) {
				return fmt.Errorf("policy must be one of %v", reviewPolicies)
			}
			if err := config.SetValue("review.policy", args[0], isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})

	limit := editFlags(&cobra.Command{
		Use:   "limit <reviewer> <calls-per-day>",
		Short: "Set a reviewer's daily call limit on each machine (0 = no limit)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 0 {
				return fmt.Errorf("limit must be a whole number ≥ 0")
			}
			if _, err := providers.Get(args[0]); err != nil {
				return err
			}
			if err := config.SetValue("reviewers."+args[0]+".daily_limit", int64(n), isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})

	edit := editFlags(&cobra.Command{
		Use:   "edit",
		Short: "Choose the review chain, models, policy and limits from menus",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := editReview(isLocal(cmd)); err != nil {
				return err
			}
			return changed(cmd)
		},
	})
	cmd.AddCommand(run, chain, set, policy, limit, edit)
	return cmd
}

// editReview asks for the chain, each reviewer's settings and the policy.
func editReview(local bool) error {
	c, err := config.Load()
	if err != nil {
		return err
	}
	var opts []ui.Option
	var defs []string
	for _, p := range providers.All() {
		if !c.ProviderEnabled(p.Name()) || !providers.Has(p, "review") {
			continue
		}
		opts = append(opts, ui.Option{Label: p.Name() + " — " + p.Description(), Value: p.Name()})
		if slices.Contains(c.Review.Chain, p.Name()) {
			defs = append(defs, p.Name())
		}
	}
	picked := []string{}
	if len(opts) > 0 {
		picked, err = ui.MultiSelect("External reviewers", "Tried in the order shown; the Claude reviewer agent is always last.", opts, defs)
		if err != nil {
			return err
		}
	}
	// Keep the existing relative order, then append newly picked ones.
	var chain []string
	for _, n := range c.Review.Chain {
		if slices.Contains(picked, n) {
			chain = append(chain, n)
		}
	}
	for _, n := range picked {
		if !slices.Contains(chain, n) {
			chain = append(chain, n)
		}
	}
	chain = append(chain, "claude")
	if err := config.SetValue("review.chain", chain, local); err != nil {
		return err
	}
	for _, n := range picked {
		rv := c.Reviewer(n)
		model, effort := rv.Model, rv.Effort
		list := availableFor(n)
		if len(list) == 0 {
			if model, err = ui.Input(n+": model", "Model id for this reviewer.", rv.Model); err != nil {
				return err
			}
		} else {
			mo := make([]ui.Option, len(list))
			for i, m := range list {
				mo[i] = ui.Option{Label: fmt.Sprintf("%-28s %s", m.ID, m.Label), Value: m.ID}
			}
			def := model
			if _, ok := models.Find(list, def); !ok {
				def = list[0].ID
			}
			if model, err = ui.Select(n+": model", "From "+n+"'s own model list.", mo, def); err != nil {
				return err
			}
			if m, ok := models.Find(list, model); ok && len(m.Efforts) > 0 {
				eo := []ui.Option{{Label: "model default", Value: ""}}
				for _, e := range m.Efforts {
					eo = append(eo, ui.Option{Label: e, Value: e})
				}
				if effort, err = ui.Select(n+": effort", "", eo, rv.Effort); err != nil {
					return err
				}
			}
		}
		lim, err := ui.Input(n+": daily limit", "Calls per day on each machine; 0 = no limit.", strconv.Itoa(rv.DailyLimit))
		if err != nil {
			return err
		}
		n64, err := strconv.ParseInt(lim, 10, 64)
		if err != nil || n64 < 0 {
			return fmt.Errorf("daily limit must be a whole number ≥ 0")
		}
		for k, v := range map[string]any{"model": model, "effort": effort, "daily_limit": n64} {
			if err := config.SetValue("reviewers."+n+"."+k, v, local); err != nil {
				return err
			}
		}
	}
	rm, err := ui.Select("Claude reviewer: model", "Read-only reviewer agent; last link of the chain.", modelOptions(), c.Models.Reviewer.Model)
	if err != nil {
		return err
	}
	re, err := ui.Select("Claude reviewer: effort", "", effortOptions(rm, false), c.Models.Reviewer.Effort)
	if err != nil {
		return err
	}
	if err := setRole("reviewer", rm, re, true, local); err != nil {
		return err
	}
	pol, err := ui.Select("When should the lead run reviews?", "", []ui.Option{
		{Label: "milestones — feature done, before release/merge, big decisions", Value: "milestones"},
		{Label: "manual — only when you ask", Value: "manual"},
		{Label: "every-commit — before every commit (uses quota fast)", Value: "every-commit"},
	}, c.Review.Policy)
	if err != nil {
		return err
	}
	return config.SetValue("review.policy", pol, local)
}

// availableFor lists a reviewer's models from its own catalog, if it has one.
func availableFor(provider string) []models.Model {
	switch provider {
	case "codex":
		list, _ := models.Codex()
		return list
	case "antigravity":
		list, _ := models.Antigravity(context.Background())
		return list
	}
	return nil
}

// validateReviewer checks a reviewer model/effort against that tool's list.
func validateReviewer(cmd *cobra.Command, provider, model, effort string) error {
	force, _ := cmd.Flags().GetBool("force")
	list := availableFor(provider)
	if len(list) == 0 || force {
		return nil
	}
	m, ok := models.Find(list, model)
	if !ok {
		return fmt.Errorf("%s does not offer %q; available: %s (or --force)", provider, model, strings.Join(models.IDs(list), ", "))
	}
	if !m.Supports(effort) {
		return fmt.Errorf("%s supports effort %s", model, strings.Join(m.Efforts, ", "))
	}
	return nil
}
