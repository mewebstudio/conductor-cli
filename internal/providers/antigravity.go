package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Antigravity runs Google's Antigravity CLI (`agy`) in print mode with plan
// mode and the sandbox on: it can read files for context but does not modify
// the repository, and conductor never grants it command permissions.
type Antigravity struct{}

func (*Antigravity) Name() string           { return "antigravity" }
func (*Antigravity) Description() string    { return "Google Antigravity CLI (agy), plan mode + sandbox" }
func (*Antigravity) Capabilities() []string { return []string{"review", "ask"} }

// RepoAccess is false: agy reads files, but headless mode cannot approve
// commands such as `git diff`, so the diff travels in the prompt.
func (*Antigravity) RepoAccess() bool { return false }
func (*Antigravity) NeedsKey() bool   { return false }

func (*Antigravity) binary() (string, error) {
	p, err := exec.LookPath("agy")
	if err != nil {
		return "", errors.New("Antigravity CLI (`agy`) not found on PATH")
	}
	return p, nil
}

func (a *Antigravity) Check() error {
	_, err := a.binary()
	return err
}

// maxInlinePrompt keeps the command line well under Windows' 32K limit;
// longer prompts are passed as a file to read.
const maxInlinePrompt = 6000

const readOnlyPreamble = "This is a read-only task. Do not write a plan, do not ask for approval and do not modify any file. Answer directly.\n\n"

var (
	agyLimited = regexp.MustCompile(`(?i)(quota|rate.?limit|resource.?exhausted|too many requests|\b429\b)`)
	agyAuth    = regexp.MustCompile(`(?i)(not (signed|logged) in|sign in|login required|unauthenti|\b401\b)`)
	agySpinner = regexp.MustCompile(`[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏][^\n\r]*`)
)

func (a *Antigravity) Run(ctx context.Context, r Request) (Status, error) {
	bin, err := a.binary()
	if err != nil {
		return NotConfigured, err
	}
	body, err := os.ReadFile(r.PromptFile)
	if err != nil {
		return Failed, err
	}
	prompt := readOnlyPreamble + string(body)
	var extra []string
	if len(prompt) > maxInlinePrompt {
		prompt = readOnlyPreamble + "Read the file " + r.PromptFile + " and follow the instructions in it exactly."
		extra = []string{"--add-dir", filepath.Dir(r.PromptFile)}
	}
	if err := ensureParent(r.OutputFile); err != nil {
		return Failed, err
	}
	args := []string{"--mode", "plan", "--sandbox", "--output-format", "text", "--print-timeout", "15m"}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	if r.Effort != "" {
		args = append(args, "--effort", r.Effort)
	}
	args = append(append(args, extra...), "--print="+prompt)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = r.Root
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	log := agySpinner.ReplaceAllString(errb.String(), "")
	_ = os.WriteFile(logPath(r.OutputFile), []byte(log), 0o644)
	if runErr == nil && strings.TrimSpace(out.String()) != "" {
		return OK, os.WriteFile(r.OutputFile, out.Bytes(), 0o644)
	}
	all := log + out.String()
	switch {
	case strings.Contains(all, "permission that headless mode cannot prompt for"):
		return Failed, fmt.Errorf("antigravity wanted to run a command, which conductor does not allow (see %s)", logPath(r.OutputFile))
	case agyLimited.MatchString(all):
		return Limited, fmt.Errorf("antigravity quota or rate limit reached (see %s)", logPath(r.OutputFile))
	case agyAuth.MatchString(all):
		return NotConfigured, fmt.Errorf("antigravity is not signed in; run `agy` once (see %s)", logPath(r.OutputFile))
	case runErr == nil:
		return Failed, errors.New("antigravity returned an empty answer")
	}
	return Failed, fmt.Errorf("antigravity failed: %v (see %s)", runErr, logPath(r.OutputFile))
}

func (*Antigravity) ReadsFiles() bool { return true }
