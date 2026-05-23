package main

import (
	"fmt"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Pro-tier App methods exposed to the Vue frontend. The auto-generated
// wailsjs/go/main/App.ts bindings let the frontend call these
// directly. Keep this file thin — actual logic lives in
// entitlement.go.

// GetEntitlement returns the current state to the frontend. Used to
// hydrate the Pro screen + the useEntitlement composable on mount.
func (a *App) GetEntitlement() EntitlementState {
	if a.entitlement == nil {
		return EntitlementState{}
	}
	return a.entitlement.State()
}

// ActivateLicenseKey verifies the user-supplied raw key and persists
// it on success. Returns a stringified error message on failure so the
// frontend can render it directly (Wails marshals Go errors into the
// `Error` half of the JS Promise rejection — but a stringified path
// works on every Wails version without depending on that detail).
func (a *App) ActivateLicenseKey(rawKey string) (EntitlementState, error) {
	if a.entitlement == nil {
		return EntitlementState{}, fmt.Errorf("entitlement subsystem not initialised")
	}
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return EntitlementState{}, fmt.Errorf("empty license key")
	}
	return a.entitlement.Activate(rawKey)
}

// DeactivateLicense clears the persisted entitlement. Used by the
// "Sign out of Pro" button in Settings. Idempotent — returns current
// state even if nothing changed.
func (a *App) DeactivateLicense() EntitlementState {
	if a.entitlement == nil {
		return EntitlementState{}
	}
	a.entitlement.Deactivate()
	return a.entitlement.State()
}

// OpenStoreURL opens the LemonSqueezy checkout for the given SKU in
// the user's default browser. Centralised here so the frontend stays
// store-URL-agnostic — when prices/URLs change, only this file is
// touched.
func (a *App) OpenStoreURL(sku string) error {
	var url string
	switch sku {
	case SKUDesktop:
		url = "https://store.privycs.com/buy/privycs_pro_desktop"
	case SKUBundle:
		url = "https://store.privycs.com/buy/privycs_pro_bundle_all"
	default:
		return fmt.Errorf("unknown SKU %q", sku)
	}
	wailsRuntime.BrowserOpenURL(a.ctx, url)
	return nil
}

// IsProUnlockedJS is the frontend-facing version of the IsProUnlocked
// helper. Returns true when gating is globally off OR the user has
// an active Pro entitlement. Frontend feature-gates call this.
func (a *App) IsProUnlockedJS() bool {
	return IsProUnlocked(a.entitlement)
}

// ProGatingEnabledJS exposes the dormant-gating constant to the
// frontend so it can hide upgrade-prompts entirely while gating is off
// (no need to even render the dialog).
func (a *App) ProGatingEnabledJS() bool {
	return ProGatingEnabled
}
