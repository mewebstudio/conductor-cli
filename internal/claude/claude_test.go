package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/ojson"
)

func sandbox(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CONDUCTOR_HOME", filepath.Join(dir, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
}

func TestSyncHookAddRemoveLeavesOthers(t *testing.T) {
	obj, _ := ojson.Parse([]byte(`{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"other"}]}]}}`))
	setSyncHook(obj, "/usr/local/bin/conductor", true)
	setSyncHook(obj, "/usr/local/bin/conductor", true) // idempotent
	out, _ := ojson.Marshal(obj)
	if n := strings.Count(string(out), `"sync"`); n != 1 {
		t.Fatalf("want one sync hook, got %d:\n%s", n, out)
	}
	setSyncHook(obj, "", false)
	out, _ = ojson.Marshal(obj)
	if strings.Contains(string(out), "conductor") || !strings.Contains(string(out), `"other"`) {
		t.Fatalf("removal touched the wrong hooks:\n%s", out)
	}
}

func TestReplaceBlock(t *testing.T) {
	doc := replaceBlock("# Mine\n", "one")
	if !strings.HasPrefix(doc, "# Mine\n\n"+blockBegin) {
		t.Fatalf("block not appended: %q", doc)
	}
	doc = replaceBlock(doc+"\ntail\n", "two")
	if strings.Contains(doc, "one") || !strings.Contains(doc, "two") || !strings.HasSuffix(doc, "tail\n") {
		t.Fatalf("block not replaced in place: %q", doc)
	}
	if got := replaceBlock(replaceBlock("", "x"), ""); got != "" {
		t.Fatalf("removing the only block should leave nothing, got %q", got)
	}
}

func TestApplyAndUninstallRestoreSettings(t *testing.T) {
	sandbox(t)
	orig := "{\n  \"theme\": \"dark\",\n  \"model\": \"claude-sonnet-5-5\"\n}\n"
	if err := os.MkdirAll(filepath.Dir(SettingsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SettingsPath(), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(c, "/opt/bin/conductor"); err != nil {
		t.Fatal(err)
	}
	st, err := Status(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st {
		if s.Action != "ok" {
			t.Fatalf("%s is %s right after apply", s.Path, s.Action)
		}
	}
	b, _ := os.ReadFile(SettingsPath())
	if !strings.Contains(string(b), `"Bash(conductor review run *)"`) {
		t.Fatalf("allow rule missing:\n%s", b)
	}
	model, effort, hook, _ := SettingsState()
	if model != "claude-opus-5-5" || effort != "high" || !hook {
		t.Fatalf("settings not applied: %s %s %v", model, effort, hook)
	}
	c.Models.Mini.Enabled = false
	if _, err := Apply(c, "/opt/bin/conductor"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(AgentPath("worker-mini")); !os.IsNotExist(err) {
		t.Fatal("disabled worker-mini was not removed")
	}
	if _, err := Uninstall(false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(SettingsPath())
	want := "{\n  \"theme\": \"dark\",\n  \"model\": \"claude-opus-5-5\",\n  \"effortLevel\": \"high\"\n}\n"
	if string(got) != want {
		t.Fatalf("settings after uninstall:\n%s", got)
	}
	if _, err := os.Stat(MemoryPath()); !os.IsNotExist(err) {
		t.Fatal("CLAUDE.md created by conductor should be removed")
	}
}

func TestRenderWithoutAddress(t *testing.T) {
	sandbox(t)
	c, _ := config.Load()
	files, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	skill := string(files[0].Data)
	if !strings.Contains(skill, "the user") || strings.Contains(skill, "**  **") {
		t.Fatalf("empty address not handled:\n%s", skill)
	}
	c.Comms.Address = "Alex"
	files, _ = Render(c)
	if !strings.Contains(string(files[0].Data), "as **Alex**") {
		t.Fatal("address not rendered")
	}
}
