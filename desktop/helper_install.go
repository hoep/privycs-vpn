package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	helperServiceName = "privycs-vpn-helper"
	helperFlag        = "--helper"
)

// InstallHelper installs the privileged helper as a system service.
// This requires a one-time admin/root prompt.
func InstallHelper() error {
	switch runtime.GOOS {
	case "linux":
		return installHelperLinux()
	case "darwin":
		return installHelperMacOS()
	case "windows":
		return installHelperWindows()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// UninstallHelper removes the privileged helper system service.
func UninstallHelper() error {
	switch runtime.GOOS {
	case "linux":
		return uninstallHelperLinux()
	case "darwin":
		return uninstallHelperMacOS()
	case "windows":
		return uninstallHelperWindows()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// IsHelperInstalled checks if the helper service is installed and running.
func IsHelperInstalled() bool {
	switch runtime.GOOS {
	case "linux":
		return isHelperInstalledLinux()
	case "darwin":
		return isHelperInstalledMacOS()
	case "windows":
		return isHelperInstalledWindows()
	default:
		return false
	}
}

// IsHelperRunning checks if the helper is actually reachable via IPC.
func IsHelperRunning() bool {
	client := NewHelperClient()
	resp, err := client.SendCommand("status", map[string]string{
		"protocol": "wireguard",
	})
	// If we can communicate, the helper is running, regardless of VPN status
	return err == nil && (resp.Success || resp.Error == "not connected")
}

// EnsureHelper checks if the helper is installed. If not, it prompts the user
// to install it (one-time admin prompt). Returns nil if helper is ready.
func EnsureHelper() error {
	if IsHelperRunning() {
		return nil
	}

	if IsHelperInstalled() {
		// Service installed but not running, try to start it
		if err := startHelperService(); err != nil {
			log.Printf("Helper service installed but failed to start: %v", err)
			return err
		}
		return nil
	}

	// Not installed — install it
	log.Println("Helper not installed, triggering installation...")
	return InstallHelper()
}

// getHelperBinaryPath returns the path to the current executable.
func getHelperBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine executable path: %w", err)
	}
	// Resolve symlinks to get the real path
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("cannot resolve executable path: %w", err)
	}
	return exe, nil
}

// ============================================================================
// Linux: systemd service
// ============================================================================

const systemdUnitPath = "/etc/systemd/system/privycs-vpn-helper.service"

func installHelperLinux() error {
	exePath, err := getHelperBinaryPath()
	if err != nil {
		return err
	}

	unitContent := fmt.Sprintf(`[Unit]
Description=Privycs VPN Privileged Helper
After=network.target

[Service]
Type=simple
ExecStart=%s %s
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, exePath, helperFlag)

	// Use pkexec for graphical admin prompt (one-time)
	cmd := exec.Command("pkexec", "bash", "-c",
		fmt.Sprintf("echo '%s' > %s && systemctl daemon-reload && systemctl enable %s && systemctl start %s",
			strings.ReplaceAll(unitContent, "'", "'\\''"),
			systemdUnitPath,
			helperServiceName,
			helperServiceName,
		))

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install helper service: %s: %w", string(out), err)
	}

	log.Printf("Helper service installed and started via systemd")
	return nil
}

func uninstallHelperLinux() error {
	cmd := exec.Command("pkexec", "bash", "-c",
		fmt.Sprintf("systemctl stop %s; systemctl disable %s; rm -f %s; systemctl daemon-reload",
			helperServiceName, helperServiceName, systemdUnitPath))

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to uninstall helper service: %s: %w", string(out), err)
	}

	log.Println("Helper service uninstalled")
	return nil
}

func isHelperInstalledLinux() bool {
	out, err := exec.Command("systemctl", "is-active", helperServiceName).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "active"
}

// ============================================================================
// macOS: LaunchDaemon plist
// ============================================================================

const launchDaemonPath = "/Library/LaunchDaemons/com.privycs.vpn-helper.plist"

func installHelperMacOS() error {
	exePath, err := getHelperBinaryPath()
	if err != nil {
		return err
	}

	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.privycs.vpn-helper</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/var/log/privycs-vpn-helper.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/privycs-vpn-helper.log</string>
</dict>
</plist>
`, exePath, helperFlag)

	// v0.9.14.19: write plist to a temp file FIRST, then AppleScript
	// only does the cp + launchctl load. Pre-fix put the entire plist
	// XML inline in the AppleScript string — the plist's many `"`
	// characters broke the AppleScript parser at the first internal
	// `"` it encountered. User reported "Helper lässt sich nicht
	// installieren" with no visible error because the AppleScript
	// failure surfaced as a generic exec error that the UI dropped.
	tmpPlist, err := os.CreateTemp("", "privycs-vpn-helper-*.plist")
	if err != nil {
		return fmt.Errorf("failed to create temp plist: %w", err)
	}
	tmpPath := tmpPlist.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpPlist.WriteString(plistContent); err != nil {
		tmpPlist.Close()
		return fmt.Errorf("failed to write temp plist: %w", err)
	}
	tmpPlist.Close()

	// AppleScript now only runs three trivial shell commands:
	// cp /tmp/file → /Library/LaunchDaemons, chown to root, launchctl
	// load. No XML embedded in the script string → no quote-escape
	// hell. Single-quoted shell within double-quoted AppleScript is
	// safe because the embedded shell command no longer contains `"`.
	script := fmt.Sprintf(
		`do shell script "cp '%s' '%s' && chown root:wheel '%s' && chmod 644 '%s' && launchctl load -w '%s'" with administrator privileges`,
		tmpPath,
		launchDaemonPath,
		launchDaemonPath,
		launchDaemonPath,
		launchDaemonPath,
	)

	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Surface the full osascript output. Common failure messages:
		//   "User canceled" — user dismissed the admin-password prompt
		//   "Operation not permitted" — TCC denied AppleEvents access;
		//     fix is to grant Privycs in Settings → Privacy & Security
		//     → Automation → System Events
		//   "launchctl: ... is not a valid LaunchDaemon" — plist parse
		//     error (should not happen with the template above)
		log.Printf("Helper install failed. osascript output:\n%s", string(out))
		return fmt.Errorf("failed to install helper daemon: %s", strings.TrimSpace(string(out)))
	}

	log.Println("Helper daemon installed and loaded via launchd")
	return nil
}

func uninstallHelperMacOS() error {
	// Minimal script — every additional command in the do-shell-script
	// chain raises the chance of TCC terminating the AppleEvent. v0.9.14.28
	// user reported "signal: terminated" because the original four-step
	// chain (bootout + unload + rm + pkill) was too long for unsigned
	// app contexts under Sequoia. Reduced to two steps + the most
	// permissive error suppression. pkill is dropped — bootout itself
	// SIGTERMs the daemon; pkill was redundant insurance that introduced
	// a TCC failure surface.
	//
	// If this still fails (typical under Sequoia for unsigned apps),
	// the fallback is the manual Terminal commands documented in the
	// Settings UI's helper-error banner. Apple's expected install path
	// for system-level VPN helpers is SMAppService + a signed bundle —
	// our osascript approach is the unsigned-app workaround that's
	// inherently fragile under modern macOS.
	script := fmt.Sprintf(
		`do shell script "launchctl bootout system '%s' 2>/dev/null; rm -f '%s'" with administrator privileges`,
		launchDaemonPath, launchDaemonPath,
	)

	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Surface a more actionable error than "signal: terminated".
		// The user's recourse for TCC-killed osascript is the manual
		// Terminal-with-sudo path; tell them so directly in the error
		// shown by the Settings UI banner.
		errMsg := strings.TrimSpace(string(out))
		if errMsg == "" {
			errMsg = err.Error()
		}
		return fmt.Errorf("uninstall via UI failed (%s) — please run the manual cleanup commands from the helper-info banner in Settings", errMsg)
	}

	log.Println("Helper daemon uninstalled")
	return nil
}

func isHelperInstalledMacOS() bool {
	// Check the LaunchDaemon plist file existence directly. The
	// pre-fix used `launchctl list com.privycs.vpn-helper` which
	// fails for system-level LaunchDaemons when run from a non-root
	// process: launchctl shows the calling user's LaunchAgents in
	// the unscoped form, but system daemons require either
	// `sudo launchctl list` or the modern `launchctl print
	// system/<label>` form. Our GUI app runs as the user, sees
	// "Could not find service" even though the daemon IS installed
	// and running, and reports installed=false. Side effect: the
	// Settings → Privileged Helper UI hid both Install AND Uninstall
	// buttons because their v-if conditions both depend on
	// helperStatus.installed.
	//
	// The plist at /Library/LaunchDaemons/ is world-readable (mode
	// 644), so a plain os.Stat works without elevation. Existence of
	// the plist is the canonical "registered with launchd" signal —
	// any further check (running vs. just installed) goes through
	// the IPC ping in IsHelperRunning.
	_, err := os.Stat(launchDaemonPath)
	return err == nil
}

// ============================================================================
// Windows: sc.exe service
// ============================================================================

const windowsServiceName = "PrivycsVPNHelper"

func installHelperWindows() error {
	exePath, err := getHelperBinaryPath()
	if err != nil {
		return err
	}

	// Use sc.exe to create the service. This needs admin privileges.
	// On Windows, the NSIS installer typically runs elevated, or we use RunAs.
	binPath := fmt.Sprintf(`"%s" %s`, exePath, helperFlag)

	cmd := execHidden("sc", "create", windowsServiceName,
		"binPath=", binPath,
		"start=", "auto",
		"DisplayName=", "Privycs VPN Helper",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Try with RunAs elevation
		psCmd := fmt.Sprintf(
			`Start-Process -FilePath sc.exe -ArgumentList 'create','%s','binPath=','\"%s\" %s','start=','auto','DisplayName=','Privycs VPN Helper' -Verb RunAs -Wait -WindowStyle Hidden`,
			windowsServiceName, exePath, helperFlag,
		)
		cmd = execHidden("powershell", "-NoProfile", "-Command", psCmd)
		out, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to create Windows service: %s: %w", string(out), err)
		}
	}

	// Start the service
	startCmd := execHidden("sc", "start", windowsServiceName)
	startCmd.CombinedOutput()

	log.Println("Helper Windows service installed and started")
	return nil
}

func uninstallHelperWindows() error {
	// Stop then delete
	execHidden("sc", "stop", windowsServiceName).Run()
	cmd := execHidden("sc", "delete", windowsServiceName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete Windows service: %s: %w", string(out), err)
	}
	log.Println("Helper Windows service uninstalled")
	return nil
}

func isHelperInstalledWindows() bool {
	out, err := execHidden("sc", "query", windowsServiceName).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "RUNNING")
}

// startHelperService attempts to start an already-installed helper service.
func startHelperService() error {
	switch runtime.GOOS {
	case "linux":
		cmd := exec.Command("systemctl", "start", helperServiceName)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl start failed: %s: %w", string(out), err)
		}
		return nil
	case "darwin":
		cmd := exec.Command("launchctl", "start", "com.privycs.vpn-helper")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl start failed: %s: %w", string(out), err)
		}
		return nil
	case "windows":
		cmd := execHidden("sc", "start", windowsServiceName)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("sc start failed: %s: %w", string(out), err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported platform")
	}
}
