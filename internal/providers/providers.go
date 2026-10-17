// Package providers runs non-Claude models (Codex, NVIDIA, user plugins) for
// read-only reviews and text-only "ask" jobs behind one contract:
//
//	run <review|ask> <prompt_file> <output_file>
//	exit 0 ok · 1 failed · 2 not configured · 3 quota or rate limited
package providers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/paths"
)

// Status is a provider run outcome; values match the plugin exit codes.
type Status int

const (
	OK            Status = 0
	Failed        Status = 1
	NotConfigured Status = 2
	Limited       Status = 3
)

func (s Status) String() string {
	switch s {
	case OK:
		return "ok"
	case NotConfigured:
		return "not-configured"
	case Limited:
		return "limited"
	default:
		return "failed"
	}
}

// Request is one provider job.
type Request struct {
	Capability string // "review" or "ask"
	PromptFile string
	OutputFile string
	Root       string // project root (working directory for the provider)
	Model      string
	Effort     string
}

// Provider is a model backend outside Claude.
type Provider interface {
	Name() string
	Description() string
	Capabilities() []string
	// RepoAccess is true when the provider can read the repository itself;
	// otherwise review prompts must carry the diff inline.
	RepoAccess() bool
	// ReadsFiles is true when the provider can open repository files on its
	// own (needed for audits, which carry no diff).
	ReadsFiles() bool
	// NeedsKey is true when the provider needs an API key in the credential store.
	NeedsKey() bool
	// Check reports why the provider cannot run right now (nil = ready).
	Check() error
	Run(ctx context.Context, r Request) (Status, error)
}

// ErrUnknown means no provider has that name.
var ErrUnknown = errors.New("unknown provider")

// Builtin providers ship inside the binary.
func Builtin() []Provider { return []Provider{&Codex{}, &Antigravity{}, &NVIDIA{}} }

// All returns built-in providers plus plugins in ~/.conductor-cli/providers, sorted by name.
func All() []Provider {
	all := Builtin()
	all = append(all, Plugins()...)
	sort.Slice(all, func(i, j int) bool { return all[i].Name() < all[j].Name() })
	return all
}

// Get returns a provider by name.
func Get(name string) (Provider, error) {
	for _, p := range All() {
		if p.Name() == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrUnknown, name)
}

// Has reports whether p supports a capability.
func Has(p Provider, capability string) bool {
	for _, c := range p.Capabilities() {
		if c == capability {
			return true
		}
	}
	return false
}

// Ready reports whether a provider is enabled in config and can run.
func Ready(c *config.Config, p Provider) error {
	if !c.ProviderEnabled(p.Name()) {
		return fmt.Errorf("disabled (enable with `conductor providers enable %s`)", p.Name())
	}
	return p.Check()
}

// logPath is where a provider's raw output goes, next to its result file.
func logPath(output string) string {
	ext := filepath.Ext(output)
	return output[:len(output)-len(ext)] + ".log"
}

func ensureParent(path string) error { return os.MkdirAll(filepath.Dir(path), 0o755) }

// pluginDir returns where a plugin named name lives.
func pluginDir(name string) string { return filepath.Join(paths.ProvidersDir(), name) }
