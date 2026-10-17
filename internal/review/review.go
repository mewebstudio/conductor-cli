// Package review runs the review chain: external providers in configured
// order, then a prepared prompt for the read-only Claude reviewer agent.
package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/providers"
)

// maxInlineDiff caps the diff pasted into prompts for providers without repo access.
const maxInlineDiff = 120_000

// Attempt records one provider try.
type Attempt struct {
	Provider string
	Status   string
	Detail   string
}

// Result is the chain outcome.
type Result struct {
	Status   string // ok | fallback | failed
	Provider string
	Output   string // findings file (ok)
	Prompt   string // prompt file for the Claude reviewer (fallback)
	Attempts []Attempt
}

// Options configure one run.
type Options struct {
	Root       string // project root
	BriefFile  string // the lead's review brief
	Base       string // git ref to diff against (working tree included)
	Chain      []string
	Capability string // review (default) or ask
}

// Run walks the chain and returns the first success or a Claude fallback.
func Run(ctx context.Context, c *config.Config, o Options) (*Result, error) {
	brief, err := os.ReadFile(o.BriefFile)
	if err != nil {
		return nil, fmt.Errorf("reading brief: %w", err)
	}
	if o.Base == "" {
		o.Base = "HEAD"
	}
	if len(o.Chain) == 0 {
		o.Chain = c.Review.Chain
	}
	dir := filepath.Join(o.Root, filepath.FromSlash(c.Docs.Reviews))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := EnsureIgnored(o.Root, c.Docs.Reviews); err != nil {
		return nil, err
	}
	stamp := time.Now().Format("20060102-150405")
	diff, untracked := gitChanges(o.Root, o.Base)

	res := &Result{Status: "failed"}
	usage := loadUsage()
	for _, name := range o.Chain {
		if name == "claude" {
			if !c.Models.Reviewer.Enabled {
				res.Attempts = append(res.Attempts, Attempt{name, "skipped", "reviewer agent disabled"})
				continue
			}
			prompt := filepath.Join(dir, stamp+"-claude.prompt.md")
			body := buildPrompt(string(brief), o.Base, diff, untracked, false)
			if err := os.WriteFile(prompt, []byte(body), 0o644); err != nil {
				return nil, err
			}
			res.Status, res.Provider, res.Prompt = "fallback", "claude", prompt
			return res, nil
		}
		p, err := providers.Get(name)
		if err != nil {
			res.Attempts = append(res.Attempts, Attempt{name, "skipped", err.Error()})
			continue
		}
		if !providers.Has(p, "review") {
			res.Attempts = append(res.Attempts, Attempt{name, "skipped", "does not support review"})
			continue
		}
		if err := providers.Ready(c, p); err != nil {
			res.Attempts = append(res.Attempts, Attempt{name, "skipped", err.Error()})
			continue
		}
		rv := c.Reviewer(name)
		if rv.DailyLimit > 0 && usage.today(name) >= rv.DailyLimit {
			res.Attempts = append(res.Attempts, Attempt{name, "skipped", fmt.Sprintf("daily limit %d reached", rv.DailyLimit)})
			continue
		}
		prompt := filepath.Join(dir, stamp+"-"+name+".prompt.md")
		out := filepath.Join(dir, stamp+"-"+name+".md")
		body := buildPrompt(string(brief), o.Base, diff, untracked, p.RepoAccess())
		if err := os.WriteFile(prompt, []byte(body), 0o644); err != nil {
			return nil, err
		}
		st, runErr := p.Run(ctx, providers.Request{
			Capability: "review", PromptFile: prompt, OutputFile: out,
			Root: o.Root, Model: rv.Model, Effort: rv.Effort,
		})
		if st != providers.NotConfigured {
			usage.add(name)
			_ = usage.save()
		}
		detail := ""
		if runErr != nil {
			detail = runErr.Error()
		}
		res.Attempts = append(res.Attempts, Attempt{name, st.String(), detail})
		if st == providers.OK {
			res.Status, res.Provider, res.Output = "ok", name, out
			return res, nil
		}
	}
	return res, nil
}

func buildPrompt(brief, base, diff, untracked string, repoAccess bool) string {
	var b strings.Builder
	b.WriteString("# Independent code review (read-only)\n\n")
	b.WriteString("You are reviewing changes made by another AI team. Do not modify any file.\n\n")
	b.WriteString("## Brief from the lead\n\n")
	b.WriteString(strings.TrimSpace(brief))
	b.WriteString("\n\n## The change\n\n")
	if repoAccess {
		fmt.Fprintf(&b, "Inspect it yourself in the repository: `git diff %s` (working tree included) and `git status`.\n", base)
		if untracked != "" {
			b.WriteString("New untracked files:\n" + untracked + "\n")
		}
	} else {
		d := diff
		if len(d) > maxInlineDiff {
			d = d[:maxInlineDiff] + "\n… diff truncated …\n"
		}
		if strings.TrimSpace(d) == "" {
			d = "(no diff against " + base + ")"
		}
		fmt.Fprintf(&b, "Diff against `%s` (working tree included):\n\n```diff\n%s\n```\n", base, d)
		if untracked != "" {
			b.WriteString("\nNew untracked files (contents not included):\n" + untracked + "\n")
		}
	}
	b.WriteString(`
## Output format
At most 10 findings, most severe first. For each:
- severity: high / medium / low
- location: path:line
- problem: one or two sentences
- fix: one or two sentences (no rewritten code)

Focus on correctness, risks, missed cases and the acceptance criteria. Skip style unless it hides a bug.
If nothing material is wrong, say so plainly. Never invent findings.
`)
	return b.String()
}

// gitChanges returns the diff against base (including the working tree) and
// the list of untracked files. Errors yield empty strings.
func gitChanges(root, base string) (diff, untracked string) {
	if out, err := git(root, "diff", base); err == nil {
		diff = out
	}
	if out, err := git(root, "ls-files", "--others", "--exclude-standard"); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if l != "" {
				untracked += "- " + l + "\n"
			}
		}
	}
	return diff, untracked
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

// ProjectRoot returns the git top-level of dir, or dir itself.
func ProjectRoot(dir string) string {
	if out, err := git(dir, "rev-parse", "--show-toplevel"); err == nil {
		if s := strings.TrimSpace(out); s != "" {
			return filepath.FromSlash(s)
		}
	}
	return dir
}

// EnsureIgnored adds dir to the project's .gitignore if it is not ignored yet.
func EnsureIgnored(root, dir string) error {
	dir = strings.Trim(filepath.ToSlash(dir), "/")
	if _, err := git(root, "check-ignore", "-q", dir+"/x"); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil // not a git repository: nothing to protect
	}
	path := filepath.Join(root, ".gitignore")
	cur, _ := os.ReadFile(path)
	s := string(cur)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	s += "\n# conductor review reports (machine-local)\n/" + dir + "/\n"
	return os.WriteFile(path, []byte(s), 0o644)
}

// usage counts provider calls per day on this machine.
type usage map[string]map[string]int

func usagePath() string { return filepath.Join(paths.StateDir(), "usage.json") }

func loadUsage() usage {
	u := usage{}
	if b, err := os.ReadFile(usagePath()); err == nil {
		_ = json.Unmarshal(b, &u)
	}
	return u
}

func day() string { return time.Now().Format("2006-01-02") }

func (u usage) today(p string) int { return u[day()][p] }

func (u usage) add(p string) {
	d := day()
	if u[d] == nil {
		u[d] = map[string]int{}
	}
	u[d][p]++
	for k := range u { // keep a week
		if t, err := time.Parse("2006-01-02", k); err == nil && time.Since(t) > 7*24*time.Hour {
			delete(u, k)
		}
	}
}

func (u usage) save() error {
	b, _ := json.MarshalIndent(u, "", "  ")
	if err := os.MkdirAll(paths.StateDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(usagePath(), b, 0o644)
}

// UsageToday returns today's call count for a provider.
func UsageToday(p string) int { return loadUsage().today(p) }

// WithinLimit reports whether a provider may be called again today.
func WithinLimit(c *config.Config, p string) bool {
	lim := c.Reviewer(p).DailyLimit
	return lim <= 0 || UsageToday(p) < lim
}

// RecordUse counts one call against a provider's daily limit.
func RecordUse(p string) {
	u := loadUsage()
	u.add(p)
	_ = u.save()
}
