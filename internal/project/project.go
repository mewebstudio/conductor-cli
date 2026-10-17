// Package project prepares a repository for conductor (decision and handoff
// docs, .conductor.conf) and links its docs into a shared Obsidian vault.
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mewebstudio/conductor-cli/internal/paths"
)

// ConfName is the per-project settings file.
const ConfName = ".conductor.conf"

// Project is a repository registered on this machine.
type Project struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func registryPath() string { return filepath.Join(paths.StateDir(), "projects.json") }

// List returns registered projects, sorted by name.
func List() []Project {
	var out []Project
	if b, err := os.ReadFile(registryPath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func save(list []Project) error {
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(paths.StateDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(registryPath(), append(b, '\n'), 0o644)
}

// Register adds or updates a project.
func Register(p Project) error {
	list := List()
	for i, x := range list {
		if x.Path == p.Path || x.Name == p.Name {
			list[i] = p
			return save(list)
		}
	}
	return save(append(list, p))
}

// Unregister removes a project by name.
func Unregister(name string) error {
	var keep []Project
	for _, p := range List() {
		if p.Name != name {
			keep = append(keep, p)
		}
	}
	return save(keep)
}

var safeName = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

// NameFor derives a vault-safe project name from its folder.
func NameFor(root string) string {
	n := strings.TrimSpace(safeName.ReplaceAllString(filepath.Base(root), "-"))
	if n == "" || n == "." || n == ".." {
		n = "project"
	}
	return n
}

// Conf is the per-project settings file.
type Conf struct {
	Test      string   `toml:"test"`
	Build     string   `toml:"build"`
	Protected []string `toml:"protected"`
}

// Detect guesses build and test commands from the files in root.
func Detect(root string) Conf {
	has := func(name string) bool { _, err := os.Stat(filepath.Join(root, name)); return err == nil }
	glob := func(p string) bool { m, _ := filepath.Glob(filepath.Join(root, p)); return len(m) > 0 }
	switch {
	case has("go.mod"):
		return Conf{Test: "go test ./...", Build: "go build ./..."}
	case has("Package.swift"):
		return Conf{Test: "swift test", Build: "swift build"}
	case glob("*.xcworkspace") || glob("*.xcodeproj"):
		return Conf{Test: "xcodebuild test -scheme <Scheme> -destination 'platform=iOS Simulator,name=iPhone 17'", Build: "xcodebuild build -scheme <Scheme>"}
	case has("Cargo.toml"):
		return Conf{Test: "cargo test", Build: "cargo build"}
	case has("package.json"):
		return Conf{Test: "npm test", Build: "npm run build"}
	case has("pyproject.toml") || has("setup.py"):
		return Conf{Test: "pytest"}
	case has("build.gradle") || has("build.gradle.kts"):
		return Conf{Test: "./gradlew test", Build: "./gradlew build"}
	}
	return Conf{}
}

// ConfText renders a .conductor.conf.
func ConfText(c Conf) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return "# Conductor project settings. Workers verify with these commands.\n" +
		"# Edit freely; `conductor project init` never overwrites this file.\n\n" +
		"test = " + q(c.Test) + "\n" +
		"build = " + q(c.Build) + "\n" +
		"# Folders workers must never modify, e.g. [\"Secrets/\", \"fastlane/\"]\n" +
		"protected = []\n"
}

// Docs creates the decisions and handoff files if missing; it returns the
// files it created.
func Docs(root, decisions, handoff string) ([]string, error) {
	var made []string
	files := map[string]string{
		decisions: "# Decisions\n\nOne entry per decision, newest first.\n\n" +
			"<!-- ## " + time.Now().Format("2006-01-02") + " — Title\n**Decision:** …\n**Why:** …\n**Alternatives considered:** … -->\n",
		handoff: "# Handoff\n\nWhere work stopped and what comes next. The lead updates this before a\nsession ends or when a usage limit is near.\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return made, err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return made, err
		}
		made = append(made, rel)
	}
	sort.Strings(made)
	return made, nil
}

// --- Obsidian vault ---

// VaultLink is where a project appears inside the vault.
func VaultLink(vault, name string) string { return filepath.Join(vault, "Projects", name) }

// LinkState of a project in the vault.
type LinkState string

const (
	LinkOK      LinkState = "linked"
	LinkMissing LinkState = "missing"
	LinkWrong   LinkState = "points elsewhere"
	LinkForeign LinkState = "a real folder is in the way"
)

// Inspect reports how a project is linked in the vault.
func Inspect(vault string, p Project, docsDir string) LinkState {
	link := VaultLink(vault, p.Name)
	fi, err := os.Lstat(link)
	if err != nil {
		return LinkMissing
	}
	target, err := filepath.EvalSymlinks(link)
	want, err2 := filepath.EvalSymlinks(filepath.Join(p.Path, docsDir))
	if err == nil && err2 == nil && filepath.Clean(target) == filepath.Clean(want) {
		return LinkOK
	}
	if fi.Mode()&os.ModeSymlink != 0 || isJunction(link) {
		return LinkWrong
	}
	return LinkForeign
}

// Link puts the project's docs folder into the vault. It never copies:
// copies would drift from the repository.
func Link(vault string, p Project, docsDir string) error {
	src := filepath.Join(p.Path, docsDir)
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	link := VaultLink(vault, p.Name)
	switch Inspect(vault, p, docsDir) {
	case LinkOK:
		return nil
	case LinkForeign:
		return fmt.Errorf("%s exists and is not a link; move it away first", link)
	case LinkWrong:
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, src).CombinedOutput(); err != nil {
			return fmt.Errorf("creating junction: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return os.Symlink(src, link)
}

// Unlink removes a project's link from the vault (never the docs themselves).
func Unlink(vault, name string) error {
	link := VaultLink(vault, name)
	fi, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 && !isJunction(link) {
		return fmt.Errorf("%s is a real folder; not removing it", link)
	}
	return os.Remove(link)
}

// isJunction detects a Windows directory junction (reported as a plain dir).
func isJunction(p string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	target, err := os.Readlink(p)
	return err == nil && target != ""
}

// WriteHome regenerates the vault's Home.md index.
func WriteHome(vault, decisions, handoff string, list []Project) error {
	var b strings.Builder
	b.WriteString("# Projects\n\n<!-- Generated by conductor; edits are overwritten. -->\n\n")
	if len(list) == 0 {
		b.WriteString("No projects yet. Run `conductor project init` inside a repository.\n")
	}
	base := func(rel string) string {
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
		if len(parts) == 2 {
			return parts[1]
		}
		return rel
	}
	for _, p := range list {
		enc := strings.ReplaceAll(p.Name, " ", "%20")
		fmt.Fprintf(&b, "- **%s** — [decisions](Projects/%s/%s) · [handoff](Projects/%s/%s)\n",
			p.Name, enc, base(decisions), enc, base(handoff))
	}
	if err := os.MkdirAll(vault, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(vault, "Home.md"), []byte(b.String()), 0o644)
}

// OpenVault opens the vault in Obsidian.
func OpenVault(vault string) error {
	uri := "obsidian://open?path=" + strings.ReplaceAll(filepath.ToSlash(vault), " ", "%20")
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", uri).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", uri).Run()
	default:
		return exec.Command("xdg-open", uri).Run()
	}
}
