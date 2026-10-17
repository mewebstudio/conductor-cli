package agents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocPathHonoursEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	l := P("CLAUDE_CONFIG_DIR", ".claude", "skills")
	if got := l.Path(); got != filepath.Join(home, ".claude", "skills") {
		t.Fatalf("default: %s", got)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/x/claude")
	if got := l.Path(); got != filepath.FromSlash("/x/claude/skills") {
		t.Fatalf("env: %s", got)
	}
}

func TestTableHasCoreAgents(t *testing.T) {
	for _, k := range []string{"claude-code", "codex", "cursor", "gemini-cli", "antigravity", "github-copilot"} {
		if _, ok := Find(k, nil); !ok {
			t.Errorf("missing agent %s", k)
		}
	}
}

func TestLinkInspectUnlink(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared", "s1")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(shared, "SKILL.md"), []byte("x"), 0o644)
	agentDir := filepath.Join(root, "agent")
	if st := Inspect(agentDir, "s1", shared); st != Missing {
		t.Fatalf("want missing, got %v", st)
	}
	if err := Link(agentDir, "s1", shared); err != nil {
		t.Fatal(err)
	}
	if st := Inspect(agentDir, "s1", shared); st != Linked && st != Copied {
		t.Fatalf("want linked, got %v", st)
	}
	if err := Link(agentDir, "s1", shared); err != nil {
		t.Fatal("linking twice must be a no-op:", err)
	}
	if err := Unlink(agentDir, "s1", shared); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(shared, "SKILL.md")); err != nil {
		t.Fatal("unlink removed the shared copy")
	}
	// Something the user put there is never replaced or removed.
	foreign := filepath.Join(agentDir, "s1")
	_ = os.MkdirAll(foreign, 0o755)
	if err := Link(agentDir, "s1", shared); err == nil {
		t.Fatal("linking over a foreign folder must fail")
	}
	_ = Unlink(agentDir, "s1", shared)
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("unlink removed a foreign folder")
	}
}
