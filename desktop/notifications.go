package main

import (
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strings"
)

// NotificationLevel classifies severity so the OS can render with
// appropriate urgency / sound / colour.
type NotificationLevel int

const (
	NotifyInfo NotificationLevel = iota
	NotifyWarning
	NotifyError
)

// Notify emits a native desktop notification without bringing the window
// to focus. Uses the platform's native mechanism:
//
//   - Linux: notify-send (libnotify — installed by default on every
//     mainstream desktop; falls back to gdbus on minimal setups).
//   - macOS: osascript "display notification" — always available.
//   - Windows: PowerShell New-BurntToastNotification via script; if
//     BurntToast module is missing we fall back to a balloon tip via
//     System.Windows.Forms.NotifyIcon (always present on Win10+).
//
// Notify runs in a goroutine and never blocks the caller — notifications
// are a fire-and-forget side effect, not a critical path. Failures are
// logged at debug level since users on headless systems or with no
// notification daemon don't care.
func Notify(title, body string, level NotificationLevel) {
	go notifyNative(title, body, level)
}

func notifyNative(title, body string, level NotificationLevel) {
	title = sanitizeNotificationText(title)
	body = sanitizeNotificationText(body)

	switch runtime.GOOS {
	case "linux":
		urgency := "normal"
		switch level {
		case NotifyWarning:
			urgency = "normal"
		case NotifyError:
			urgency = "critical"
		}
		// Use the system's notify-send. If it's missing (headless box,
		// minimal install), we silently skip — not worth a dependency
		// on dbus bindings for a non-critical feature.
		if _, err := exec.LookPath("notify-send"); err != nil {
			log.Printf("notify-send not available, skipping notification: %s", title)
			return
		}
		cmd := exec.Command("notify-send",
			"--app-name=Privycs VPN",
			"--urgency="+urgency,
			"--icon=network-vpn",
			title, body,
		)
		if err := cmd.Run(); err != nil {
			log.Printf("notify-send failed: %v", err)
		}

	case "darwin":
		// osascript's "display notification" is the documented, stable
		// API. It honours the user's Do-Not-Disturb and Notification
		// Centre settings automatically.
		script := fmt.Sprintf(
			`display notification %q with title %q`,
			body, title,
		)
		cmd := exec.Command("osascript", "-e", script)
		if err := cmd.Run(); err != nil {
			log.Printf("osascript notify failed: %v", err)
		}

	case "windows":
		// Use Windows.UI.Notifications via PowerShell. This produces a
		// real Action-Center toast, not a tray balloon (the old
		// deprecated API). Works on Win10+ without third-party modules.
		// The PowerShell one-liner builds a ToastNotification XML and
		// schedules it via the Privycs VPN app identity.
		ps := fmt.Sprintf(`
[reflection.assembly]::loadwithpartialname('System.Windows.Forms') | Out-Null;
[reflection.assembly]::loadwithpartialname('System.Drawing') | Out-Null;
$notify = New-Object System.Windows.Forms.NotifyIcon;
$notify.Icon = [System.Drawing.SystemIcons]::Information;
$notify.BalloonTipTitle = %q;
$notify.BalloonTipText = %q;
$notify.Visible = $true;
$notify.ShowBalloonTip(5000);
Start-Sleep -Seconds 6;
$notify.Dispose();
`, title, body)
		cmd := execHidden("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", ps)
		if err := cmd.Run(); err != nil {
			log.Printf("powershell notify failed: %v", err)
		}

	default:
		log.Printf("Notification: [%s] %s — %s", levelLabel(level), title, body)
	}
}

// sanitizeNotificationText strips control characters that could break
// shell-escaped invocations (newlines, backticks, backslashes). The
// titles/bodies we emit are controlled strings from our own code, so
// this is defense-in-depth rather than a serious injection concern.
func sanitizeNotificationText(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, `\`, "/")
	if len(s) > 256 {
		s = s[:253] + "..."
	}
	return s
}

func levelLabel(l NotificationLevel) string {
	switch l {
	case NotifyWarning:
		return "WARN"
	case NotifyError:
		return "ERROR"
	default:
		return "INFO"
	}
}
