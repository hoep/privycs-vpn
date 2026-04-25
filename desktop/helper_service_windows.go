//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
)

// privycsVPNService adapts PrivilegedHelper to Windows Service Control Manager.
// Without this, Go binaries invoked via `sc start` never report "Running" status
// and SCM times out with Error 1053 after 30s.
type privycsVPNService struct {
	helper *PrivilegedHelper
}

// Execute is called by svc.Run when the service starts. It must:
//  1. Report StartPending → start the helper listener → report Running.
//  2. Listen for Stop/Shutdown requests from SCM and shut the helper down cleanly.
func (s *privycsVPNService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	// helper.Start() blocks as long as the listener accepts connections.
	// Run it in a goroutine so we can report Running back to SCM immediately.
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.helper.Start()
	}()

	// Give Start() a brief moment to bind the socket. If binding fails fast,
	// we catch the error here and tell SCM the service didn't start.
	select {
	case err := <-errCh:
		log.Printf("helper.Start failed immediately: %v", err)
		changes <- svc.Status{State: svc.Stopped}
		return false, 1
	case <-time.After(500 * time.Millisecond):
		// Socket bound successfully.
	}

	const accepts = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.Running, Accepts: accepts}
	log.Println("Helper reported Running to SCM")

	// Migration cleanup for existing installs: prior installer versions
	// configured the SCM service with `failure ... actions=
	// restart/5000/restart/5000/restart/5000`, which on every helper
	// crash auto-respawned the service three times within 60s. Any
	// crash that left partial WFP/firewall state would then have its
	// state collide with the next attempt's setup, leaking handles
	// in wf.dll / npfs.sys and correlating with BSOD on user
	// machines. The new installer no longer sets this, but existing
	// installs already have the old failure config baked in — clear
	// it once at runtime here. Safe to call repeatedly; safe to fail
	// (e.g. permission denied on locked-down systems): we just log.
	go func() {
		// Wait so this can never delay the SCM Running handshake.
		time.Sleep(2 * time.Second)
		out, err := execHidden(
			"sc.exe", "failure", "PrivycsVPNHelper",
			"reset=", "0", "actions=", "",
		).CombinedOutput()
		if err != nil {
			log.Printf("SCM failure-action cleanup failed (non-fatal): %v: %s", err, out)
		} else {
			log.Println("SCM auto-restart cleared for PrivycsVPNHelper (migration ok)")
		}
	}()

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				log.Printf("SCM requested stop (cmd=%d)", c.Cmd)
				s.helper.Stop()
				changes <- svc.Status{State: svc.StopPending}
				return false, 0
			default:
				log.Printf("Unexpected SCM command: %d", c.Cmd)
			}
		case err := <-errCh:
			if err != nil {
				log.Printf("helper.Start exited with error: %v", err)
			}
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

// runHelperEntrypoint decides whether to run under Windows SCM (sc start)
// or in console mode (developer invokes `privycs-vpn.exe --helper` manually).
func runHelperEntrypoint() {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		// Console/interactive mode — no SCM handshake needed.
		RunHelperMode()
		return
	}

	// Set up logging to a file before SCM takes over stdout/stderr.
	programData := os.Getenv("PROGRAMDATA")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	logPath := filepath.Join(programData, "PrivycsVPN", "helper.log")
	os.MkdirAll(filepath.Dir(logPath), 0755)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		log.SetOutput(f)
		// NOTE: don't Close() f — the service runs for the lifetime of the process.
	}
	log.SetFlags(log.Ldate | log.Ltime)
	log.Println("Privycs VPN helper starting under Windows SCM")

	helper := NewPrivilegedHelper()
	runner := &privycsVPNService{helper: helper}
	if err := svc.Run("PrivycsVPNHelper", runner); err != nil {
		log.Printf("svc.Run failed: %v", err)
	}
}
