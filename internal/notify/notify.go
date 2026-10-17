// Package notify shows a desktop notification on a best-effort basis.
package notify

import (
	"os/exec"
	"runtime"
	"strings"
)

// Send shows title/message; it returns an error only when no notifier exists.
func Send(title, message string) error {
	switch runtime.GOOS {
	case "darwin":
		script := `display notification ` + appleQuote(message) + ` with title ` + appleQuote(title)
		return exec.Command("osascript", "-e", script).Run()
	case "windows":
		ps := `[void][Reflection.Assembly]::LoadWithPartialName('System.Windows.Forms');` +
			`$n=New-Object System.Windows.Forms.NotifyIcon;` +
			`$n.Icon=[System.Drawing.SystemIcons]::Information;$n.Visible=$true;` +
			`$n.ShowBalloonTip(6000,` + psQuote(title) + `,` + psQuote(message) + `,'Info');` +
			`Start-Sleep -Seconds 7;$n.Dispose()`
		return exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", ps).Start()
	default:
		return exec.Command("notify-send", title, message).Run()
	}
}

func appleQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func psQuote(s string) string { return `'` + strings.ReplaceAll(s, `'`, `''`) + `'` }
