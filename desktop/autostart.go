package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// SetAutostart enables or disables autostart at OS login.
// Linux: .desktop file in ~/.config/autostart/
// macOS: Launch Agent plist in ~/Library/LaunchAgents/
// Windows: Registry key in HKCU\Software\Microsoft\Windows\CurrentVersion\Run
func SetAutostart(enable bool) error {
	switch runtime.GOOS {
	case "linux":
		return setAutostartLinux(enable)
	case "darwin":
		return setAutostartMacOS(enable)
	case "windows":
		return setAutostartWindows(enable)
	default:
		return fmt.Errorf("autostart not supported on %s", runtime.GOOS)
	}
}

// IsAutostartEnabled checks if autostart is currently configured
func IsAutostartEnabled() bool {
	switch runtime.GOOS {
	case "linux":
		path := autostartDesktopPath()
		_, err := os.Stat(path)
		return err == nil
	case "darwin":
		path := launchAgentPath()
		_, err := os.Stat(path)
		return err == nil
	case "windows":
		out, err := execHidden("reg", "query",
			`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
			"/v", "PrivycsVPN").CombinedOutput()
		return err == nil && len(out) > 0
	}
	return false
}

// --- Linux ---

func autostartDesktopPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "autostart", "privycs-vpn.desktop")
}

func setAutostartLinux(enable bool) error {
	path := autostartDesktopPath()

	if !enable {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove autostart: %w", err)
		}
		log.Println("Autostart: disabled (Linux)")
		return nil
	}

	// Find the executable path
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Privycs VPN
Exec=%s
Icon=privycs-vpn
Comment=Privycs VPN Client
Categories=Network;
Terminal=false
StartupNotify=false
X-GNOME-Autostart-enabled=true
`, exePath)

	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write autostart file: %w", err)
	}
	log.Println("Autostart: enabled (Linux)")
	return nil
}

// --- macOS ---

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "com.privycs.vpn.plist")
}

func setAutostartMacOS(enable bool) error {
	path := launchAgentPath()

	if !enable {
		// Unload and remove
		exec.Command("launchctl", "unload", path).Run()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove launch agent: %w", err)
		}
		log.Println("Autostart: disabled (macOS)")
		return nil
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// For macOS app bundles, the executable is inside .app/Contents/MacOS/
	// The open command launches the app properly
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.privycs.vpn</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
</dict>
</plist>
`, exePath)

	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.WriteFile(path, []byte(plist), 0644); err != nil {
		return fmt.Errorf("failed to write launch agent: %w", err)
	}
	exec.Command("launchctl", "load", path).Run()
	log.Println("Autostart: enabled (macOS)")
	return nil
}

// --- Windows ---

func setAutostartWindows(enable bool) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	if !enable {
		cmd := execHidden("reg", "delete",
			`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
			"/v", "PrivycsVPN", "/f")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to remove autostart registry key: %w", err)
		}
		log.Println("Autostart: disabled (Windows)")
		return nil
	}

	cmd := execHidden("reg", "add",
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
		"/v", "PrivycsVPN",
		"/t", "REG_SZ",
		"/d", exePath,
		"/f")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set autostart registry key: %w", err)
	}
	log.Println("Autostart: enabled (Windows)")
	return nil
}
