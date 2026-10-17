package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRepo(t *testing.T) {
	for _, tc := range []struct{ in, url, sub string }{
		{"avdlee/swiftui-agent-skill", "https://github.com/avdlee/swiftui-agent-skill.git", ""},
		{"owner/repo/skills/foo", "https://github.com/owner/repo.git", "skills/foo"},
		{"https://github.com/owner/repo/tree/main/skills/foo", "https://github.com/owner/repo.git", "skills/foo"},
		{"git@github.com:owner/repo.git", "git@github.com:owner/repo.git", ""},
	} {
		url, sub, ok := ParseRepo(tc.in)
		if !ok || url != tc.url || sub != tc.sub {
			t.Errorf("%s → %q %q %v", tc.in, url, sub, ok)
		}
	}
	for _, bad := range []string{"swiftui-expert-skill", "./local", "name@market"} {
		if _, _, ok := ParseRepo(bad); ok {
			t.Errorf("%s should not parse as a repository", bad)
		}
	}
}

func TestPluginID(t *testing.T) {
	if !IsPluginID("figma@claude-plugins-official") || IsPluginID("owner/repo") || IsPluginID("git@github.com:a/b") {
		t.Fatal("plugin id detection is wrong")
	}
}

func TestCheckName(t *testing.T) {
	for _, bad := range []string{"..", "../x", "a/b", "", ".hidden"} {
		if CheckName(bad) == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if CheckName("swiftui-expert_skill.v2") != nil {
		t.Error("valid name rejected")
	}
}

func TestDiscoverAndFrontmatter(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("skills/a/SKILL.md", "---\nname: alpha\ndescription: \"First one\"\n---\nbody")
	write("skills/b/SKILL.md", "no frontmatter")
	write("node_modules/x/SKILL.md", "---\nname: ignored\n---")
	found, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Name != "alpha" || found[0].Description != "First one" || found[0].Path != "skills/a" || found[1].Name != "b" {
		t.Fatalf("unexpected: %+v", found)
	}
}

func TestSelectionRoundTripOmitsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUCTOR_CONFIG_DIR", dir)
	s := &Selection{}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "skills.toml"))
	if string(b) != "# Your skills and plugins. Managed by `conductor skills`.\n\n" {
		t.Fatalf("empty selection should write no keys:\n%s", b)
	}
	s.Put(Skill{Name: "b", Source: "o/r"})
	s.Put(Skill{Name: "a", Source: "o/r", Path: "x"})
	s.Put(Skill{Name: "a", Source: "o/r", Path: "y"})
	s.PutPlugin(Plugin{ID: "p@m"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 2 || got.Skills[0].Name != "a" || got.Skills[0].Path != "y" || len(got.Plugins) != 1 {
		t.Fatalf("round trip: %+v", got)
	}
	if !got.Drop("p") || len(got.Plugins) != 0 {
		t.Fatal("dropping a plugin by bare name failed")
	}
}
