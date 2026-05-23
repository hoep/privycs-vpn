package main

import (
	"fmt"
)

// Pro-tier feature gates. Mirror the Android v0.9.15.77 layout: six
// gates, each implemented as a small helper that returns an error
// when the user is on free tier AND the action would push them past
// the free-tier limit. While ProGatingEnabled=false (dormant), every
// gate is a no-op — IsProUnlocked returns true and the gates pass.
//
// Each gate returns a string error message rather than a typed error
// so the frontend can render the localised UpgradeDialog content via
// errors.Is is not required here — the frontend just shows the dialog
// and lets the user click "View Pro".

// gateError is the canonical "feature is Pro-only" error returned by
// gates. The frontend recognises this prefix and shows the Pro
// upgrade dialog rather than a plain toast.
const proGateErrPrefix = "PRO_REQUIRED:"

// proGate wraps the boolean entitlement check + free-tier-limit
// counter. The featureLabel is what the frontend renders inside the
// upgrade dialog ("Multi-protocol", "Network rules" etc.). Caller
// should return the resulting error early if non-nil.
func (a *App) proGate(currentCount, freeLimit int, featureLabel string) error {
	if IsProUnlocked(a.entitlement) {
		return nil
	}
	if currentCount < freeLimit {
		return nil
	}
	return fmt.Errorf("%s %s", proGateErrPrefix, featureLabel)
}

// proGate1Required — the simpler variant where there's no count, just
// "this entire feature requires Pro". Used by Gateway-Download and the
// per-pool split-tunnel toggle.
func (a *App) proGate1Required(featureLabel string) error {
	if IsProUnlocked(a.entitlement) {
		return nil
	}
	return fmt.Errorf("%s %s", proGateErrPrefix, featureLabel)
}

// --- Gate definitions (one per feature, called at the entry point of
// each App method that performs the gated action). ---

// gateMultiProtocol triggers when the target connection already has
// any protocol configured and the user is trying to ADD another one.
// Free tier = one protocol per connection.
func (a *App) gateMultiProtocol(connectionID string) error {
	if connectionID == "" {
		// new connection — covered by gateMultiConfig instead
		return nil
	}
	if a.connections == nil {
		return nil
	}
	conn := a.connections.Get(connectionID)
	if conn == nil || len(conn.Protocols) == 0 {
		return nil
	}
	return a.proGate(len(conn.Protocols), 1, "multiProtocol")
}

// gateMultiConfig triggers when the user already has at least one
// connection and is creating another. Free tier = one connection.
func (a *App) gateMultiConfig(connectionID string) error {
	if connectionID != "" {
		// updating an existing slot — not a new connection
		return nil
	}
	if a.connections == nil {
		return nil
	}
	return a.proGate(len(a.connections.List()), 1, "multiConfig")
}

// gateNetworkRules triggers on any AddNetworkRule call when the
// existing rule count is at or above the free-tier limit. Free tier =
// zero custom rules (the COD-converted single "Wi-Fi only" rule is
// allowed implicitly because it pre-exists from settings migration).
func (a *App) gateNetworkRules() error {
	if a.networkRules == nil {
		return nil
	}
	return a.proGate(len(a.networkRules.List()), 0, "networkRules")
}

// gateGatewayDownload triggers on FetchMyProfile / FetchMyConfig /
// DownloadAndImportConfig — the gateway "Pull configs from server"
// feature. Free tier = the user can still IMPORT a config file
// manually; only the one-click Gateway-download is gated.
func (a *App) gateGatewayDownload() error {
	return a.proGate1Required("gatewayDownload")
}

// gatePoolCreate triggers on CreatePoolFromUploads / CreatePoolFromPaths.
// Free tier = zero pools.
func (a *App) gatePoolCreate() error {
	if a.pools == nil {
		return nil
	}
	return a.proGate(len(a.pools.List()), 0, "pools")
}

// gateSplitTunnel triggers when the user tries to configure per-pool
// split-tunnel rules. Frontend hides the section by default while
// gating is dormant; the backend gate is the second line of defence.
func (a *App) gateSplitTunnel() error {
	return a.proGate1Required("splitTunnel")
}
