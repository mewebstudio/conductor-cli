package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mewebstudio/conductor-cli/internal/config"
	"github.com/mewebstudio/conductor-cli/internal/paths"
	"github.com/mewebstudio/conductor-cli/internal/ui"
)

// binDir is where self-install puts the binary.
func binDir() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(paths.Home(), "AppData", "Local")
		}
		return filepath.Join(base, "Programs", "conductor")
	}
	return filepath.Join(paths.Home(), ".local", "bin")
}

func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.EqualFold(filepath.Clean(p), filepath.Clean(dir)) {
			return true
		}
	}
	return false
}

func selfInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "self-install",
		Short: "Copy this binary to your user bin folder and put it on PATH",
		Long: "macOS/Linux: ~/.local/bin/conductor.\n" +
			"Windows: %LOCALAPPDATA%\\Programs\\conductor\\conductor.exe, added to your user PATH (no admin rights).",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			src, err := os.Executable()
			if err != nil {
				return err
			}
			if r, err := filepath.EvalSymlinks(src); err == nil {
				src = r
			}
			dir := binDir()
			name := "conductor"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			dst := filepath.Join(dir, name)
			if filepath.Clean(src) != filepath.Clean(dst) {
				data, err := os.ReadFile(src)
				if err != nil {
					return err
				}
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				tmp := dst + ".new"
				if err := os.WriteFile(tmp, data, 0o755); err != nil {
					return err
				}
				if runtime.GOOS == "windows" {
					_ = os.Remove(dst + ".old")
					_ = os.Rename(dst, dst+".old") // a running exe can be renamed, not overwritten
				}
				if err := os.Rename(tmp, dst); err != nil {
					return err
				}
			}
			ui.Okf("installed %s", dst)
			if !onPath(dir) {
				if runtime.GOOS == "windows" {
					ps := fmt.Sprintf(`$p=[Environment]::GetEnvironmentVariable('Path','User'); if (($p -split ';') -notcontains '%s') { [Environment]::SetEnvironmentVariable('Path', ($p.TrimEnd(';') + ';%s'), 'User') }`, dir, dir)
					if err := exec.Command("powershell", "-NoProfile", "-Command", ps).Run(); err != nil {
						ui.Warnf("could not update PATH: %v; add %s yourself", err, dir)
					} else {
						ui.Okf("added %s to your user PATH; open a new terminal", dir)
					}
				} else {
					ui.Warnf("%s is not on PATH; add this to your shell profile:  export PATH=\"%s:$PATH\"", dir, dir)
				}
			}
			if config.Exists() {
				// Point the session hook at the installed binary.
				out, err := exec.Command(dst, "apply").CombinedOutput()
				if err != nil {
					ui.Warnf("re-applying with the installed binary failed: %s", strings.TrimSpace(string(out)))
				}
			}
			return nil
		},
	}
}
