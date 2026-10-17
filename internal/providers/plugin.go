package providers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/secrets"
)

// Plugin is a user-installed provider described by
// ~/.conductor-cli/providers/<name>/provider.toml:
//
//	name         = "antigravity"
//	description  = "Google Antigravity CLI"
//	capabilities = ["review", "ask"]
//	command      = "run.sh"        # relative to the plugin folder, or on PATH
//	args         = []              # extra args placed before <cap> <prompt> <output>
//	needs_key    = false           # store one with `conductor providers key <name>`
//	repo_access  = true            # false: review prompts include the diff inline
//
// The command receives CONDUCTOR_MODEL, CONDUCTOR_EFFORT, CONDUCTOR_ROOT and,
// when needs_key is set, CONDUCTOR_API_KEY, and exits 0/1/2/3 like built-ins.
type Plugin struct {
	Dir      string   `toml:"-"`
	PName    string   `toml:"name"`
	Desc     string   `toml:"description"`
	Caps     []string `toml:"capabilities"`
	Command  string   `toml:"command"`
	Args     []string `toml:"args"`
	Key      bool     `toml:"needs_key"`
	RepoRead bool     `toml:"repo_access"`
}

// Plugins loads every valid plugin; invalid ones are skipped (doctor reports them).
func Plugins() []Provider {
	var out []Provider
	for _, p := range scanPlugins() {
		if p.err == nil {
			out = append(out, p.plugin)
		}
	}
	return out
}

type scanned struct {
	dir    string
	plugin *Plugin
	err    error
}

func scanPlugins() []scanned {
	entries, _ := os.ReadDir(paths.ProvidersDir())
	var out []scanned
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(paths.ProvidersDir(), e.Name())
		p, err := LoadPlugin(dir)
		out = append(out, scanned{dir, p, err})
	}
	return out
}

// PluginProblems lists plugin folders that failed to load.
func PluginProblems() map[string]error {
	out := map[string]error{}
	for _, s := range scanPlugins() {
		if s.err != nil {
			out[s.dir] = s.err
		}
	}
	return out
}

// LoadPlugin reads and validates a plugin folder.
func LoadPlugin(dir string) (*Plugin, error) {
	p := &Plugin{Dir: dir}
	if _, err := toml.DecodeFile(filepath.Join(dir, "provider.toml"), p); err != nil {
		return nil, err
	}
	switch {
	case p.PName == "":
		p.PName = filepath.Base(dir)
	case p.PName == "codex" || p.PName == "nvidia" || p.PName == "antigravity" || p.PName == "claude":
		return nil, fmt.Errorf("plugin name %q is reserved", p.PName)
	}
	if p.Command == "" {
		return nil, errors.New("provider.toml: command is required")
	}
	if len(p.Caps) == 0 {
		return nil, errors.New("provider.toml: capabilities is required")
	}
	return p, nil
}

func (p *Plugin) Name() string           { return p.PName }
func (p *Plugin) Description() string    { return p.Desc }
func (p *Plugin) Capabilities() []string { return p.Caps }
func (p *Plugin) RepoAccess() bool       { return p.RepoRead }
func (p *Plugin) NeedsKey() bool         { return p.Key }

func (p *Plugin) binary() (string, error) {
	local := filepath.Join(p.Dir, p.Command)
	if fi, err := os.Stat(local); err == nil && !fi.IsDir() {
		return local, nil
	}
	return exec.LookPath(p.Command)
}

func (p *Plugin) Check() error {
	if _, err := p.binary(); err != nil {
		return fmt.Errorf("command %q not found", p.Command)
	}
	if p.Key && !secrets.Has(p.PName) {
		return fmt.Errorf("no API key stored (run `conductor providers key %s`)", p.PName)
	}
	return nil
}

func (p *Plugin) Run(ctx context.Context, r Request) (Status, error) {
	bin, err := p.binary()
	if err != nil {
		return NotConfigured, err
	}
	if err := ensureParent(r.OutputFile); err != nil {
		return Failed, err
	}
	logFile, err := os.Create(logPath(r.OutputFile))
	if err != nil {
		return Failed, err
	}
	defer logFile.Close()
	args := append(append([]string{}, p.Args...), r.Capability, r.PromptFile, r.OutputFile)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = r.Root
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(),
		"CONDUCTOR_MODEL="+r.Model,
		"CONDUCTOR_EFFORT="+r.Effort,
		"CONDUCTOR_ROOT="+r.Root,
	)
	if p.Key {
		key, err := secrets.Get(p.PName)
		if err != nil {
			return NotConfigured, err
		}
		cmd.Env = append(cmd.Env, "CONDUCTOR_API_KEY="+key)
	}
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		st := Status(exitErr.ExitCode())
		if st < Failed || st > Limited {
			st = Failed
		}
		return st, fmt.Errorf("%s exited with %d (see %s)", p.PName, exitErr.ExitCode(), logPath(r.OutputFile))
	}
	if err != nil {
		return Failed, err
	}
	return OK, nil
}
