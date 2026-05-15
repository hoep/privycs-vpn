//go:build windows

package main

// OpenVPN Interactive Service client (Windows). v0.9.15.54.
//
// Why this exists
// ---------------
// On Windows 10.0.26200 + OpenVPN 2.7.1, when we spawn openvpn.exe
// directly from the privileged helper the process runs with
// `interactive service msg_channel=0` — meaning OpenVPN performs every
// privileged network operation (netsh DNS, netsh IPv6 address, route
// add, …) by shelling out to `C:\WINDOWS\system32\netsh.exe` itself.
// OpenVPN-2.7.1-DCO's direct-netsh code is broken on this Windows
// build: it issues a duplicate `add dns` after `set dns`, the IPv6
// `set address` form it uses fails with error 1, etc. Every one of
// these makes OpenVPN exit "due to fatal error". Filtering each broken
// netsh call individually is unbounded whack-a-mole.
//
// The correct fix — what the official OpenVPN-GUI and OpenVPN Connect
// do — is to NOT spawn openvpn.exe ourselves. Instead we connect to
// the OpenVPN Interactive Service control pipe and ask IT to spawn
// openvpn.exe. The service launches openvpn with `--msg-channel
// <handle>` so `msg_channel != 0`; OpenVPN then delegates every
// privileged op back to the interactive service over that channel,
// and the service's own (working) implementation runs them instead
// of OpenVPN's broken direct-netsh path. One structural fix resolves
// the entire class.
//
// Wire protocol (reverse-engineered from OpenVPN's
// src/openvpnserv/interactive.c + openvpn-gui openvpn.c, verified
// against both):
//
//   pipe : \\.\pipe\openvpn\service   (message-type pipe; community
//          OpenVPN 2.x. OpenVPN3/Connect uses \\.\pipe\ovpnagent
//          with a different protocol — not us.)
//
//   request (one message, UTF-16LE, three NUL-terminated strings):
//          <working-directory>\0<options>\0<stdin>\0
//      - working-directory: dir the .ovpn lives in
//      - options: the openvpn command line WITHOUT the leading
//        "openvpn" token (the service prepends its own openvpn.exe
//        and appends --msg-channel). Standard CreateProcess quoting:
//        wrap any path containing a space in double quotes.
//      - stdin: management password, empty for us (we use an
//        unauthenticated local management port). Still NUL-terminated.
//
//   response (one message, UTF-16LE text):
//          0x%08x\n...   first line is the hex error code (0 == ok).
//      On success the service's ReturnProcessId emits
//          0x00000000\n0x%08x\n<description>
//      i.e. line 2 is the spawned openvpn PID in hex. On failure
//      ReturnError emits 0x<errcode>\n<function>\n<message>.
//
// After a successful spawn openvpn behaves exactly as before from our
// side — it still opens the --management socket we passed and writes
// the --log file we passed, so protocol_openvpn.go's existing
// log-tail status detection is unchanged. The interactive service
// owns the openvpn process lifetime; our windows_dns_set helper
// action becomes belt-and-braces (harmless: a redundant single
// `netsh set dns` if the service already set it correctly).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// openvpnInteractivePipe is the well-known control pipe the community
// OpenVPN 2.x Interactive Service ("OpenVPNServiceInteractive")
// listens on. Installed + started by the standard OpenVPN-Windows
// MSI. If the service isn't installed/running, DialPipe fails fast
// and the caller falls back to the legacy direct-spawn path.
const openvpnInteractivePipe = `\\.\pipe\openvpn\service`

// utf16leNUL encodes s as UTF-16LE bytes followed by a UTF-16 NUL
// (0x00 0x00). No BOM — the interactive service expects raw LE.
func utf16leNUL(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2+2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	b = append(b, 0x00, 0x00) // terminating wide NUL
	return b
}

// decodeUTF16LE turns interactive-service response bytes back into a
// Go string, tolerating a trailing odd byte and embedded NULs.
func decodeUTF16LE(b []byte) string {
	n := len(b) &^ 1 // round down to even
	u := make([]uint16, 0, n/2)
	for i := 0; i < n; i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

// quoteArg applies the minimal CreateProcess/CommandLineToArgvW
// quoting the interactive service needs: wrap in double quotes if the
// token contains whitespace or a quote; escape embedded quotes and
// the backslashes that precede them per the documented algorithm.
// OpenVPN config paths under %LOCALAPPDATA% routinely contain spaces
// (e.g. C:\Users\First Last\AppData\...), so this matters.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			backslashes++
		case '"':
			// escape all pending backslashes (they precede a quote)
			// then the quote itself
			for ; backslashes > 0; backslashes-- {
				b.WriteString(`\\`)
			}
			b.WriteString(`\"`)
		default:
			for ; backslashes > 0; backslashes-- {
				b.WriteByte('\\')
			}
			b.WriteByte(s[i])
		}
	}
	// trailing backslashes must be doubled so the closing quote
	// isn't escaped
	for ; backslashes > 0; backslashes-- {
		b.WriteString(`\\`)
	}
	b.WriteByte('"')
	return b.String()
}

// buildOptionsLine joins openvpn arguments into the single
// command-line string the interactive service expects (it prepends
// "openvpn" and appends "--msg-channel <h>"). args must NOT include
// the openvpn binary path.
func buildOptionsLine(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = quoteArg(a)
	}
	return strings.Join(parts, " ")
}

// startOpenVPNViaInteractiveService asks the OpenVPN Interactive
// Service to spawn openvpn with the given args (binary path NOT
// included — the service uses its own openvpn.exe from the same
// install). configDir is the working directory. Returns the spawned
// openvpn PID.
//
// Returns an error if the service pipe is unavailable (caller should
// fall back to the legacy direct-spawn path) or if the service
// refused the start (returned a non-zero error code).
func startOpenVPNViaInteractiveService(configDir string, args []string) (int, error) {
	timeout := 5 * time.Second
	// CRITICAL: connect at SecurityImpersonation level. The OpenVPN
	// Interactive Service calls ImpersonateNamedPipeClient() +
	// OpenThreadToken() to determine which user to spawn openvpn.exe
	// as and to authorise the request. go-winio's DialPipe /
	// DialPipeAccess default to PipeImpLevelAnonymous — at that level
	// OpenThreadToken fails with ERROR_CANT_OPEN_ANONYMOUS (0x543)
	// and the service refuses the start ("code=0x00000543
	// OpenThreadToken"). DialPipeAccessImpLevel with
	// PipeImpLevelImpersonation lets the service impersonate our
	// (LocalSystem helper) thread, which is what openvpn-gui /
	// OpenVPN Connect do.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := winio.DialPipeAccessImpLevel(
		ctx,
		openvpnInteractivePipe,
		uint32(windows.GENERIC_READ|windows.GENERIC_WRITE),
		winio.PipeImpLevelImpersonation,
	)
	if err != nil {
		return 0, fmt.Errorf("interactive service pipe unavailable: %w", err)
	}
	defer conn.Close()

	// One request message: dir\0 options\0 stdin\0  (all UTF-16LE).
	var msg []byte
	msg = append(msg, utf16leNUL(configDir)...)
	msg = append(msg, utf16leNUL(buildOptionsLine(args))...)
	msg = append(msg, utf16leNUL("")...) // empty management password / stdin

	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err == nil {
		// best-effort; winio supports deadlines
	}
	if _, err := conn.Write(msg); err != nil {
		return 0, fmt.Errorf("interactive service write failed: %w", err)
	}

	// Single response message. Message-type server + winio byte-mode
	// client: one Read returns the whole message.
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return 0, fmt.Errorf("interactive service read failed: %w", err)
	}
	resp := decodeUTF16LE(buf[:n])
	return parseInteractiveResponse(resp)
}

// parseInteractiveResponse decodes the service reply. Format on
// success (ReturnProcessId): "0x%08x\n0x%08x\n<desc>" — line 0 error
// code (0), line 1 the PID in hex. On failure (ReturnError):
// "0x%08x\n<function>\n<message>".
func parseInteractiveResponse(resp string) (int, error) {
	lines := strings.Split(strings.TrimRight(resp, "\x00"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return 0, fmt.Errorf("interactive service: empty response %q", resp)
	}
	errCode, perr := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(lines[0]), "0x"), 16, 32)
	if perr != nil {
		return 0, fmt.Errorf("interactive service: unparseable status line %q", lines[0])
	}
	if errCode != 0 {
		detail := ""
		if len(lines) > 1 {
			detail = strings.TrimSpace(strings.Join(lines[1:], " "))
		}
		return 0, fmt.Errorf("interactive service refused start: code=0x%08x %s", errCode, detail)
	}
	if len(lines) < 2 {
		return 0, fmt.Errorf("interactive service: ok but no PID line in %q", resp)
	}
	pid, perr := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(lines[1]), "0x"), 16, 32)
	if perr != nil || pid == 0 {
		return 0, fmt.Errorf("interactive service: bad PID line %q", lines[1])
	}
	return int(pid), nil
}
