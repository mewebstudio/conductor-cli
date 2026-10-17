package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mewebstudio/conductor-cli/internal/ojson"
	"github.com/mewebstudio/conductor-cli/internal/paths"
)

func pluginName(id string) string { n, _, _ := strings.Cut(id, "@"); return n }

// IsPluginID reports whether arg looks like name@marketplace.
func IsPluginID(arg string) bool {
	n, m, ok := strings.Cut(arg, "@")
	return ok && n != "" && m != "" && !strings.ContainsAny(arg, "/:\\ ")
}

// InstalledPlugins returns the plugin ids enabled in Claude's settings.json.
func InstalledPlugins() map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(paths.ClaudeDir(), "settings.json"))
	if err != nil {
		return out
	}
	obj, err := ojson.Parse(b)
	if err != nil {
		return out
	}
	v, _ := obj.Get("enabledPlugins")
	if ep, ok := v.(*ojson.Object); ok {
		for _, m := range ep.Members {
			if on, _ := m.Value.(bool); on {
				out[m.Key] = true
			}
		}
	}
	return out
}

// MarketplaceSource returns the source of a marketplace Claude Code knows
// ("owner/repo" for GitHub ones), or "" if unknown.
func MarketplaceSource(name string) string {
	b, err := os.ReadFile(filepath.Join(paths.ClaudeDir(), "plugins", "known_marketplaces.json"))
	if err != nil {
		return ""
	}
	obj, err := ojson.Parse(b)
	if err != nil {
		return ""
	}
	v, ok := obj.Get(name)
	if !ok {
		return ""
	}
	entry, _ := v.(*ojson.Object)
	if entry == nil {
		return ""
	}
	sv, _ := entry.Get("source")
	src, _ := sv.(*ojson.Object)
	if src == nil {
		return ""
	}
	for _, k := range []string{"repo", "url", "path"} {
		if x, ok := src.Get(k); ok {
			if s, _ := x.(string); s != "" {
				return s
			}
		}
	}
	return ""
}

func claudeBin() (string, error) {
	p, err := exec.LookPath("claude")
	if err != nil {
		return "", errors.New("Claude Code (`claude`) not found on PATH")
	}
	return p, nil
}

func runClaude(ctx context.Context, args ...string) (string, error) {
	bin, err := claudeBin()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Stdin = nil
	err = cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// ErrNeedsApproval means the plugin declares a command Claude Code wants a
// person to approve; conductor never approves it on your behalf.
var ErrNeedsApproval = errors.New("needs your approval in Claude Code")

// InstallPlugin adds the marketplace when needed and installs the plugin.
func InstallPlugin(ctx context.Context, p Plugin) error {
	if InstalledPlugins()[p.ID] {
		return nil
	}
	_, mk, _ := strings.Cut(p.ID, "@")
	if MarketplaceSource(mk) == "" {
		if p.Marketplace == "" {
			return fmt.Errorf("marketplace %q is unknown here and no source is recorded", mk)
		}
		if out, err := runClaude(ctx, "plugin", "marketplace", "add", p.Marketplace); err != nil {
			return fmt.Errorf("adding marketplace %s: %v: %s", p.Marketplace, err, lastLine(out))
		}
	}
	out, err := runClaude(ctx, "plugin", "install", p.ID, "--scope", "user")
	if err != nil {
		if strings.Contains(strings.ToLower(out), "accept") || strings.Contains(strings.ToLower(out), "command") {
			return fmt.Errorf("%w: run `claude plugin install %s` yourself", ErrNeedsApproval, p.ID)
		}
		return fmt.Errorf("installing %s: %v: %s", p.ID, err, lastLine(out))
	}
	return nil
}

// UninstallPlugin removes a plugin.
func UninstallPlugin(ctx context.Context, id string) error {
	if !InstalledPlugins()[id] {
		return nil
	}
	if out, err := runClaude(ctx, "plugin", "uninstall", id, "--scope", "user"); err != nil {
		return fmt.Errorf("uninstalling %s: %v: %s", id, err, lastLine(out))
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
