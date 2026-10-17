package agents

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/mewebstudio/conductor-cli/internal/fsutil"
)

// LinkState describes an agent's entry for one skill.
type LinkState int

const (
	Missing LinkState = iota
	Linked            // points at the shared copy
	Copied            // a copy conductor made (Windows fallback)
	Foreign           // something else is there; never touched
)

// Inspect reports what an agent folder holds for a skill.
func Inspect(agentDir, name, shared string) LinkState {
	p := filepath.Join(agentDir, name)
	if _, err := os.Lstat(p); err != nil {
		return Missing
	}
	if fsutil.LinkPointsTo(p, shared) {
		return Linked
	}
	if !fsutil.IsLink(p) {
		if _, err := os.Stat(filepath.Join(p, copyMarker)); err == nil {
			return Copied
		}
	}
	return Foreign
}

const copyMarker = ".conductor-copy"

// Link makes agentDir/name point at shared: a symlink on macOS/Linux, a
// directory junction on Windows (no admin rights needed), or a marked copy.
func Link(agentDir, name, shared string) error {
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(agentDir, name)
	switch Inspect(agentDir, name, shared) {
	case Linked:
		return nil
	case Foreign:
		return fmt.Errorf("%s already exists and was not installed by conductor", dst)
	case Copied:
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	}
	if runtime.GOOS == "windows" {
		if err := exec.Command("cmd", "/c", "mklink", "/J", dst, shared).Run(); err == nil {
			return nil
		}
	} else if rel, err := filepath.Rel(agentDir, shared); err == nil {
		if err := os.Symlink(rel, dst); err == nil {
			return nil
		}
	}
	if err := CopyDir(shared, dst); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, copyMarker), []byte("managed by conductor\n"), 0o644)
}

// Unlink removes agentDir/name only if conductor put it there.
func Unlink(agentDir, name, shared string) error {
	dst := filepath.Join(agentDir, name)
	switch Inspect(agentDir, name, shared) {
	case Linked:
		return os.Remove(dst) // removes the link or junction, never its target
	case Copied:
		return os.RemoveAll(dst)
	}
	return nil
}

// CopyDir copies a skill folder (without .git or symlinks).
func CopyDir(src, dst string) error { return fsutil.CopyTree(src, dst, true) }
