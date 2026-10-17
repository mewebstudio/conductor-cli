// Package ui wraps interactive prompts and plain output.
package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"
)

// ErrNotInteractive means a prompt was needed but stdin is not a terminal.
var ErrNotInteractive = errors.New("this step needs an interactive terminal; run the command yourself in a terminal")

// Interactive reports whether we can prompt the user.
func Interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// Option is a menu entry.
type Option struct{ Label, Value string }

// Select asks for one value; def is preselected.
func Select(title, desc string, opts []Option, def string) (string, error) {
	if !Interactive() {
		return "", ErrNotInteractive
	}
	v := def
	hopts := make([]huh.Option[string], len(opts))
	for i, o := range opts {
		hopts[i] = huh.NewOption(o.Label, o.Value)
	}
	err := huh.NewSelect[string]().Title(title).Description(desc).Options(hopts...).Value(&v).Height(min(len(opts)+2, 14)).Run()
	return v, err
}

// MultiSelect asks for several values; defs are preselected.
func MultiSelect(title, desc string, opts []Option, defs []string) ([]string, error) {
	if !Interactive() {
		return nil, ErrNotInteractive
	}
	v := append([]string{}, defs...)
	hopts := make([]huh.Option[string], len(opts))
	for i, o := range opts {
		sel := false
		for _, d := range defs {
			sel = sel || d == o.Value
		}
		hopts[i] = huh.NewOption(o.Label, o.Value).Selected(sel)
	}
	err := huh.NewMultiSelect[string]().Title(title).Description(desc).Options(hopts...).Value(&v).Run()
	return v, err
}

// Confirm asks yes/no.
func Confirm(title, desc string, def bool) (bool, error) {
	if !Interactive() {
		return false, ErrNotInteractive
	}
	v := def
	err := huh.NewConfirm().Title(title).Description(desc).Affirmative("Yes").Negative("No").Value(&v).Run()
	return v, err
}

// Input asks for text.
func Input(title, desc, def string) (string, error) {
	if !Interactive() {
		return "", ErrNotInteractive
	}
	v := def
	err := huh.NewInput().Title(title).Description(desc).Value(&v).Run()
	return strings.TrimSpace(v), err
}

// Secret asks for a hidden value; it never echoes and never touches shell history.
func Secret(title, desc string) (string, error) {
	if !Interactive() {
		return "", ErrNotInteractive
	}
	var v string
	err := huh.NewInput().Title(title).Description(desc).EchoMode(huh.EchoModePassword).Value(&v).Run()
	return strings.TrimSpace(v), err
}

// Linef prints one line.
func Linef(format string, a ...any) { fmt.Printf(format+"\n", a...) }

// Okf, Warnf and Failf print status lines.
func Okf(format string, a ...any)   { fmt.Printf("  ✓ "+format+"\n", a...) }
func Warnf(format string, a ...any) { fmt.Printf("  ! "+format+"\n", a...) }
func Failf(format string, a ...any) { fmt.Printf("  ✗ "+format+"\n", a...) }
