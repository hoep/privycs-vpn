// Copyright (c) 2026 Privycs
// Distributed under the terms of the containing application's license.
package de.blinkt.openvpn.core;

import android.content.Context;

/**
 * Access shim that exposes the package-private StatusListener.init(Context)
 * to our main-process Application class, which lives in com.privycs.vpn and
 * therefore cannot call it directly.
 *
 * ics-openvpn's ICSOpenVPNApplication normally performs this init on every
 * app start - we don't extend that Application class (we already extend
 * StrongSwanApplication for IPSec), so PrivycsApp.onCreate invokes this
 * bridge instead. Without it the cross-process VpnStatus bridge never
 * binds, and every OpenVPN state/log/byte-count event from the :openvpn
 * subprocess is dropped on the floor.
 *
 * Keeping the bridge in a separate tiny class (rather than subclassing
 * StatusListener) avoids dragging Application lifecycle onto our caller
 * and keeps the surface area minimal: one static method, no fields.
 */
public final class PrivycsStatusListenerBridge {

    private PrivycsStatusListenerBridge() {
    }

    /**
     * Allocate a StatusListener and invoke its package-private init(Context).
     * Returns the instance so the caller can keep a hard reference and
     * prevent the AIDL ServiceConnection inside from being GC'd.
     */
    public static StatusListener install(Context applicationContext) {
        StatusListener listener = new StatusListener();
        listener.init(applicationContext);
        return listener;
    }
}
