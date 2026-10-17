package providers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"

	"github.com/mewebstudio/conductor-cli/internal/paths"
)

// Codex runs OpenAI's Codex CLI (ChatGPT plan) in a read-only, ephemeral sandbox.
type Codex struct{}

func (*Codex) Name() string           { return "codex" }
func (*Codex) Description() string    { return "OpenAI Codex CLI (ChatGPT plan), read-only sandbox" }
func (*Codex) Capabilities() []string { return []string{"review", "ask"} }
func (*Codex) RepoAccess() bool       { return true }
func (*Codex) NeedsKey() bool         { return false }

// Binary finds the codex executable: PATH first, then (on Windows) the copy
// bundled with the VS Code ChatGPT extension.
func (*Codex) Binary() (string, error) {
	if p, err := exec.LookPath("codex"); err == nil {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		pattern := filepath.Join(paths.Home(), ".vscode", "extensions", "openai.chatgpt-*", "bin", "windows-*", "codex.exe")
		if m, _ := filepath.Glob(pattern); len(m) > 0 {
			sort.Strings(m)
			return m[len(m)-1], nil
		}
	}
	return "", errors.New("codex CLI not found on PATH")
}

func (c *Codex) Check() error {
	_, err := c.Binary()
	return err
}

var (
	codexLimited = regexp.MustCompile(`(?i)(rate.?limit|usage.?limit|quota|too many requests|\b429\b)`)
	codexAuth    = regexp.MustCompile(`(?i)(not logged in|please log ?in|unauthori[sz]ed|\b401\b|authentication)`)
)

func (c *Codex) Run(ctx context.Context, r Request) (Status, error) {
	bin, err := c.Binary()
	if err != nil {
		return NotConfigured, err
	}
	in, err := os.Open(r.PromptFile)
	if err != nil {
		return Failed, err
	}
	defer in.Close()
	if err := ensureParent(r.OutputFile); err != nil {
		return Failed, err
	}
	logFile, err := os.Create(logPath(r.OutputFile))
	if err != nil {
		return Failed, err
	}
	defer logFile.Close()

	args := []string{"exec"}
	if r.Model != "" {
		args = append(args, "-m", r.Model)
	}
	if r.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+r.Effort)
	}
	args = append(args, "-s", "read-only", "--skip-git-repo-check", "--ephemeral",
		"-C", r.Root, "-o", r.OutputFile, "-")
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = in
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	runErr := cmd.Run()
	if runErr == nil {
		if fi, err := os.Stat(r.OutputFile); err == nil && fi.Size() > 0 {
			return OK, nil
		}
		return Failed, errors.New("codex finished without writing a result")
	}
	log, _ := os.ReadFile(logPath(r.OutputFile))
	switch {
	case codexLimited.Match(log):
		return Limited, fmt.Errorf("codex quota or rate limit reached (see %s)", logPath(r.OutputFile))
	case codexAuth.Match(log):
		return NotConfigured, fmt.Errorf("codex is not signed in; run `codex login` (see %s)", logPath(r.OutputFile))
	}
	return Failed, fmt.Errorf("codex failed: %v (see %s)", runErr, logPath(r.OutputFile))
}
