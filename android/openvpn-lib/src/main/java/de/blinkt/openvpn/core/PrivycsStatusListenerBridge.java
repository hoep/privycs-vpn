// Copyright (c) 2026 Privycs
// Distributed under the terms of the containing application's license.
package de.blinkt.openvpn.core;

import android.content.Context;
import android.content.SharedPreferences;
import de.blinkt.openvpn.VpnProfile;
import java.util.HashSet;
import java.util.Set;

/**
 * Access shim for package-private ics-openvpn core APIs.
 *
 * 1. `install(context)` - exposes StatusListener.init for PrivycsApp
 *    (our Application class is in com.privycs.vpn and cannot reach
 *    package-private members of de.blinkt.openvpn.core directly).
 *
 * 2. `persistProfileSync(context, profile)` - synchronously writes the
 *    profile + updates the "vpnlist" StringSet in SharedPreferences
 *    using .commit() instead of vendor's default .apply(). Required so
 *    the :openvpn subprocess can see the new profile on its first
 *    ProfileManager.get() call instead of looping 100 x 100ms in
 *    "Used x 101 tries to get current version (-1/1)" before giving up.
 *    MODE_MULTI_PROCESS only cross-syncs when the backing file has
 *    already been flushed to disk; apply() is async and on modern
 *    Android (Q+) the flag is explicitly deprecated, so relying on
 *    apply() plus retry-until-propagated is unreliable and takes at
 *    least one full flush cycle in the best case.
 */
public final class PrivycsStatusListenerBridge {

    private PrivycsStatusListenerBridge() {
    }

    public static StatusListener install(Context applicationContext) {
        StatusListener listener = new StatusListener();
        listener.init(applicationContext);
        return listener;
    }

    /**
     * Add + save + register-in-list the profile and block until the
     * SharedPreferences edit has been written to disk. After this
     * returns, any other process of the app can find the profile via
     * ProfileManager.get on the first try.
     */
    public static void persistProfileSync(Context context, VpnProfile profile) {
        ProfileManager pm = ProfileManager.getInstance(context);
        pm.addProfile(profile);
        // Writes {uuid}.vp AND increments profile.mVersion from 0 to 1.
        ProfileManager.saveProfile(context, profile);

        // Write the vpnlist StringSet with .commit() (synchronous) so the
        // OpenVPNStatusService in :openvpn can see the UUID on its next
        // loadVPNList() call. The name "ProfileManager" matches
        // ProfileManager.PREFS_NAME (package-private constant).
        SharedPreferences prefs = Preferences.getSharedPreferencesMulti("ProfileManager", context);
        Set<String> uuids = new HashSet<>();
        for (VpnProfile p : pm.getProfiles()) {
            uuids.add(p.getUUID().toString());
        }
        SharedPreferences.Editor ed = prefs.edit();
        ed.putStringSet("vpnlist", uuids);
        // Mirror vendor's counter bump from saveProfileList so any code that
        // watches for changes sees the edit.
        int counter = prefs.getInt("counter", 0);
        ed.putInt("counter", counter + 1);
        ed.commit();
    }
}
