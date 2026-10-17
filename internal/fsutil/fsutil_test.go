package fsutil

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBackupRel(t *testing.T) {
	home := filepath.FromSlash("/home/u")
	if got := backupRel(home, filepath.Join(home, ".claude", "settings.json")); got != filepath.Join(".claude", "settings.json") {
		t.Fatalf("inside home: %s", got)
	}
	got := backupRel(home, filepath.FromSlash("/tmp/x/settings.json"))
	if got != filepath.Join("_root", "tmp", "x", "settings.json") {
		t.Fatalf("outside home: %s", got)
	}
	if runtime.GOOS == "windows" {
		got := backupRel(`C:\Users\me`, `D:\work\settings.json`)
		if strings.Contains(got, ":") || got != filepath.Join("_root", "D", "work", "settings.json") {
			t.Fatalf("drive letter leaked: %s", got)
		}
	}
}
