// Package fsutil writes files conductor manages: it backs up anything it is
// about to change and records what it wrote so `doctor` can spot hand edits.
package fsutil

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mewebstudio/conductor-cli/internal/paths"
)

var (
	stampOnce sync.Once
	stamp     string
)

// runStamp names this run's backup folder.
func runStamp() string {
	stampOnce.Do(func() { stamp = time.Now().Format("20060102-150405") })
	return stamp
}

// Backup copies path into this run's backup folder, keeping its location
// relative to the home directory. Missing files are ignored.
func Backup(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(paths.Home(), path)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = strings.TrimLeft(filepath.ToSlash(path), "/:")
	}
	dst := filepath.Join(paths.BackupsDir(), runStamp(), rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	return dst, os.WriteFile(dst, data, 0o644)
}

// Result says what WriteIfChanged did.
type Result int

const (
	Unchanged Result = iota
	Created
	Updated
)

func (r Result) String() string {
	return [...]string{"unchanged", "created", "updated"}[r]
}

// WriteIfChanged writes data to path only when content differs, backing up
// the previous version first.
func WriteIfChanged(path string, data []byte, mode os.FileMode) (Result, error) {
	old, err := os.ReadFile(path)
	switch {
	case err == nil && bytes.Equal(old, data):
		return Unchanged, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return Unchanged, err
	}
	res := Created
	if err == nil {
		res = Updated
		if _, err := Backup(path); err != nil {
			return Unchanged, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Unchanged, err
	}
	tmp := path + ".conductor-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return Unchanged, err
	}
	return res, os.Rename(tmp, path)
}

// Hash returns the hex SHA-256 of data.
func Hash(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// Manifest maps managed file paths to the hash conductor last wrote.
type Manifest map[string]string

func manifestPath() string { return filepath.Join(paths.StateDir(), "managed.json") }

// LoadManifest reads the manifest (empty if missing).
func LoadManifest() Manifest {
	m := Manifest{}
	if b, err := os.ReadFile(manifestPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// Save writes the manifest.
func (m Manifest) Save() error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.StateDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(manifestPath(), append(b, '\n'), 0o644)
}

// Paths returns the managed paths, sorted.
func (m Manifest) Paths() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// CopyTree copies a directory tree without following symlinks; skipGit
// leaves out .git folders.
func CopyTree(src, dst string, skipGit bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if skipGit && d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type()&os.ModeSymlink != 0:
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
