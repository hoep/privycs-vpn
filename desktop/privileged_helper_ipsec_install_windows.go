//go:build windows

package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// safeWindowsFilenameComponent — letters, digits, hyphen, underscore.
// Anything else collapses to "_". Used to build the temp-script filename
// from the connection name without inviting path traversal.
var safeWindowsFilenameComponent = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// pkcs12Base64Pattern — matches a long base64 run that is most likely
// PKCS#12 cert material in the script body. Used to redact captured
// stdout/stderr before writing to the helper log. Tighter than
// "any base64" so we don't redact short hashes / commit SHAs that
// the script may print.
var pkcs12Base64Pattern = regexp.MustCompile(`[A-Za-z0-9+/]{60,}={0,2}`)

// cmdIPSecInstallWindowsProfile — runs a server-generated polyglot
// .cmd/.ps1 setup script that fully provisions the Windows RAS VPN
// for an IPSec connection (PKCS#12 import to LocalMachine\My, CA
// import to Trusted Root, Add-VpnConnection with MachineCertificate
// + SplitTunneling, ~300 Add-VpnConnectionRoute calls for bypass-
// complement + IPv6, rasphone.pbk patching for DisableClassBased
// DefaultRoute + IpInterfaceMetric, DNS-pin /32 host routes).
//
// Trigger: the App-side DownloadAndImportConfig calls this exactly
// once per IPSec gateway-pull on Windows. The flow on other paths
// (file-picker import, non-Windows OS) leaves the script untouched
// and falls back to the older client-side Add-VpnConnection +
// post-rasdial route install (the v1.0.5.16 path).
//
// Why "execute the script" rather than "extract directives and
// re-implement client-side": the gateway is the single source of
// truth for bypass-network calculation, cert bundle layout, server
// endpoints, and IPv6 complement math. Re-implementing any of that
// in the client would mean keeping two implementations in sync
// across releases.
//
// Args:
//   - connection_name : the user-visible connection name (used only
//     for the temp-script filename component — sanitised)
//   - script_b64      : base64-encoded script bytes (the wire-format
//     avoids JSON-escape issues with the .cmd content which contains
//     newlines, quotes, and the long PKCS#12 base64 literal)
//
// Security:
//   - The script is written under %ProgramData%\PrivycsVPN\ — a path
//     whose default ACL on Windows is "Administrators + SYSTEM full,
//     Authenticated Users read+execute". The helper runs as SYSTEM,
//     so the file is readable only by Administrators + SYSTEM during
//     its brief lifetime. NOT %TEMP% (that is per-user, not SYSTEM-
//     owned). The file is always deleted via defer — even on panic.
//   - Captured stdout/stderr is redacted before logging: any base64
//     run of 60+ chars (the PKCS#12 marker) gets replaced with
//     [REDACTED] so cert material does not leak into helper.log.
//   - The script's own internal auto-elevation logic is a no-op when
//     invoked from SYSTEM context — we just run cmd.exe /c "<path>"
//     and rely on the script's polyglot detection to delegate to
//     PowerShell with -ExecutionPolicy Bypass.
func (h *PrivilegedHelper) cmdIPSecInstallWindowsProfile(cmd HelperCommand) HelperResponse {
	connName := strings.TrimSpace(cmd.Args["connection_name"])
	if connName == "" {
		return HelperResponse{Success: false, Error: "connection_name required"}
	}

	scriptB64 := cmd.Args["script_b64"]
	if scriptB64 == "" {
		return HelperResponse{Success: false, Error: "script_b64 required"}
	}
	script, err := base64.StdEncoding.DecodeString(scriptB64)
	if err != nil {
		return HelperResponse{Success: false, Error: fmt.Sprintf("base64 decode: %v", err)}
	}
	if len(script) < 100 {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("decoded script suspiciously short (%d bytes)", len(script)),
		}
	}

	// Build target dir + path.
	programData := os.Getenv("PROGRAMDATA")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	targetDir := filepath.Join(programData, "PrivycsVPN")
	if mkErr := os.MkdirAll(targetDir, 0700); mkErr != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("create target dir %s: %v", targetDir, mkErr),
		}
	}

	safeConn := safeWindowsFilenameComponent.ReplaceAllString(connName, "_")
	if len(safeConn) > 40 {
		safeConn = safeConn[:40]
	}
	scriptPath := filepath.Join(
		targetDir,
		fmt.Sprintf("setup-%s-%d.cmd", safeConn, time.Now().UnixNano()),
	)

	// Always remove on exit. Cert material lives in the file, so any
	// successful or failed exit must clean up.
	defer func() {
		if rmErr := os.Remove(scriptPath); rmErr != nil && !os.IsNotExist(rmErr) {
			log.Printf("ipsec_install_windows_profile: cleanup of %s failed: %v", scriptPath, rmErr)
		}
	}()

	if wErr := os.WriteFile(scriptPath, script, 0600); wErr != nil {
		return HelperResponse{
			Success: false,
			Error:   fmt.Sprintf("write script: %v", wErr),
		}
	}

	// Execute. The polyglot detects cmd-mode and self-loads as
	// PowerShell with -ExecutionPolicy Bypass; we just need cmd.exe.
	// Combined output gives us a single buffer for redaction.
	log.Printf("ipsec_install_windows_profile: executing %s for connection %q", scriptPath, connName)
	out, runErr := exec.Command("cmd.exe", "/c", scriptPath).CombinedOutput()
	redacted := pkcs12Base64Pattern.ReplaceAllString(string(out), "[REDACTED]")
	// Trim and truncate the captured output for log sanity — full
	// script output for a 300-route install can be tens of KB.
	logTail := strings.TrimSpace(redacted)
	if len(logTail) > 4000 {
		logTail = logTail[:2000] + "\n... [truncated for log] ...\n" + logTail[len(logTail)-2000:]
	}

	if runErr != nil {
		exitCode := -1
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		log.Printf(
			"ipsec_install_windows_profile: FAILED connection=%q exit=%d err=%v output=\n%s",
			connName, exitCode, runErr, logTail,
		)
		// Surface a short, structured error to the caller. The full
		// (redacted) output is in helper.log for post-mortem.
		return HelperResponse{
			Success: false,
			Error: fmt.Sprintf(
				"setup script exit %d: %v (tail: %s)",
				exitCode, runErr, lastNonEmptyLine(logTail, 200),
			),
		}
	}

	log.Printf(
		"ipsec_install_windows_profile: OK connection=%q (script %d bytes) output=\n%s",
		connName, len(script), logTail,
	)
	return HelperResponse{
		Success: true,
		Output: fmt.Sprintf(
			"installed RAS VPN for %q via gateway-supplied setup script (%d bytes)",
			connName, len(script),
		),
	}
}

// lastNonEmptyLine — pick the most diagnostic line from the tail of
// captured output for the short structured-error string. Useful when
// the script's own "ERROR: ..." line is what the user sees, not the
// generic "powershell exit 1".
func lastNonEmptyLine(s string, maxLen int) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if len(line) > maxLen {
			line = line[:maxLen] + "…"
		}
		return line
	}
	return ""
}
