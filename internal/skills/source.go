package skills

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mewebstudio/conductor-cli/internal/paths"
)

var ownerRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(/.+)?$`)

// IsRemote reports whether source names a git repository.
func IsRemote(source string) bool {
	return strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://") ||
		strings.HasPrefix(source, "git@") || strings.HasPrefix(source, "ssh://")
}

// ParseRepo splits "owner/repo[/sub/path]" or a git URL into a clone URL and
// an optional sub-path.
func ParseRepo(arg string) (url, sub string, ok bool) {
	if IsRemote(arg) {
		// https://github.com/owner/repo/tree/<branch>/<path> → repo + path
		if m := regexp.MustCompile(`^(https://github\.com/[^/]+/[^/]+?)(?:\.git)?/tree/[^/]+/(.+)$`).FindStringSubmatch(arg); m != nil {
			return m[1] + ".git", strings.Trim(m[2], "/"), true
		}
		return arg, "", true
	}
	if ownerRepo.MatchString(arg) && !strings.HasPrefix(arg, ".") {
		parts := strings.SplitN(arg, "/", 3)
		url = "https://github.com/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git") + ".git"
		if len(parts) == 3 {
			sub = strings.Trim(parts[2], "/")
		}
		return url, sub, true
	}
	return "", "", false
}

// CloneURL returns the git URL for a stored source.
func CloneURL(source string) string {
	if u, _, ok := ParseRepo(source); ok {
		return u
	}
	return source
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func cacheDirFor(url string) string {
	name := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(strings.TrimSuffix(url, ".git"), "_")
	return filepath.Join(paths.CacheDir(), "repos", name)
}

// Fetch checks out ref (or the default branch when ref is "") of a repository
// into the cache and returns the folder and the exact commit.
func Fetch(ctx context.Context, url, ref string) (dir, commit string, err error) {
	dir = cacheDirFor(url)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", "", err
		}
		if _, err := git(ctx, dir, "init", "-q"); err != nil {
			return "", "", err
		}
		if _, err := git(ctx, dir, "remote", "add", "origin", url); err != nil {
			return "", "", err
		}
	}
	want := ref
	if want == "" {
		want = "HEAD"
	}
	if _, err := git(ctx, dir, "fetch", "-q", "--depth", "1", "origin", want); err != nil {
		return "", "", fmt.Errorf("fetching %s: %w", url, err)
	}
	if _, err := git(ctx, dir, "checkout", "-q", "--force", "FETCH_HEAD"); err != nil {
		return "", "", err
	}
	commit, err = git(ctx, dir, "rev-parse", "HEAD")
	return dir, commit, err
}

// Found is a skill discovered in a folder.
type Found struct {
	Name        string
	Description string
	Path        string // relative to the repository root, "" for the root
}

// Discover lists every SKILL.md under root (skipping .git and node_modules).
func Discover(root string) ([]Found, error) {
	var out []Found
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		dir := filepath.Dir(p)
		name, desc := Frontmatter(p)
		if name == "" {
			name = filepath.Base(dir)
		}
		rel, _ := filepath.Rel(root, dir)
		if rel == "." {
			rel = ""
		}
		out = append(out, Found{Name: name, Description: desc, Path: filepath.ToSlash(rel)})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

// Frontmatter reads name and description from a SKILL.md.
func Frontmatter(path string) (name, desc string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return "", ""
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			name = v
		case "description":
			desc = v
		}
	}
	return name, desc
}

// validName keeps skill names safe to use as folder names.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// CheckName rejects names that could escape the skills folder.
func CheckName(name string) error {
	if !validName.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("unsafe skill name %q", name)
	}
	return nil
}

// ChangeSummary lists files changed under path between two commits. Both are
// fetched shallowly; if history is unavailable it says so.
func ChangeSummary(ctx context.Context, repo, from, to, path string) string {
	if from == "" {
		return "  (no pinned version yet; this pins the latest)"
	}
	if _, err := git(ctx, repo, "fetch", "-q", "--depth", "1", "origin", from); err != nil {
		return "  (previous version no longer available upstream)"
	}
	args := []string{"diff", "--stat", "--stat-width=80", from, to}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := git(ctx, repo, args...)
	if err != nil || out == "" {
		return "  (no changes inside this skill's folder)"
	}
	return "  " + strings.ReplaceAll(out, "\n", "\n  ")
}
