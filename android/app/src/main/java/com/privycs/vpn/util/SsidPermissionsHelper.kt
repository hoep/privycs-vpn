package com.privycs.vpn.util

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.location.LocationManager
import android.net.Uri
import android.os.Build
import android.provider.Settings
import androidx.core.content.ContextCompat

/**
 * SSID-detection requirements bundle (v0.9.15.31).
 *
 * Android Wi-Fi APIs gate SSID reads behind three independent
 * gates. Users who report "Cannot determine SSID" with the
 * Location permission visibly granted are typically hit by one of
 * the other two:
 *
 *   1. Location PERMISSION (ACCESS_FINE_LOCATION) — the runtime
 *      permission shown in System → Privacy → Permission manager.
 *   2. Location SERVICES — the OS-wide toggle in System →
 *      Location. Even with the permission granted, if location
 *      services are OFF the WifiManager / TransportInfo APIs
 *      return "<unknown ssid>" or "" instead of the real SSID.
 *   3. NEARBY_WIFI_DEVICES (Android 13+) — narrower runtime
 *      permission added alongside the privacy refactor. Granted
 *      together with FINE_LOCATION it makes SSID reads work even
 *      when Location services are off on some Android-13+ ROMs.
 *
 * The Settings screen surfaces each independently with a status
 * pill (Granted / Denied / OS-toggle off) and an action that
 * either fires the system permission dialog or opens the
 * relevant Settings page (App-info for revoked permissions,
 * Location-settings for the OS-wide toggle).
 */
object SsidPermissionsHelper {

    fun hasFineLocationPermission(context: Context): Boolean {
        return ContextCompat.checkSelfPermission(
            context,
            Manifest.permission.ACCESS_FINE_LOCATION,
        ) == PackageManager.PERMISSION_GRANTED
    }

    /**
     * Pre-Android-13: returns true (permission did not exist;
     * SSID access was solely Location-gated).
     */
    fun hasNearbyWifiPermission(context: Context): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            return true
        }
        return ContextCompat.checkSelfPermission(
            context,
            Manifest.permission.NEARBY_WIFI_DEVICES,
        ) == PackageManager.PERMISSION_GRANTED
    }

    /**
     * OS-wide Location SERVICES toggle. Different from the
     * permission grant — even with permission granted, if this
     * is off WifiManager hides the SSID.
     */
    fun isLocationServicesEnabled(context: Context): Boolean {
        val lm = context.getSystemService(Context.LOCATION_SERVICE) as? LocationManager
            ?: return false
        // Explicit SDK_INT gate so the lint check sees the API-28
        // call is properly guarded — try/catch alone passes
        // runtime but fails Android Lint's NewApi rule.
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            try {
                lm.isLocationEnabled
            } catch (_: Throwable) {
                false
            }
        } else {
            // Pre-API-28 (API 26-27): probe individual providers.
            // Android-O minSdk path. Wrapped in try/catch because
            // isProviderEnabled can throw IllegalArgumentException
            // on some manufacturer ROMs that strip a provider.
            try {
                lm.isProviderEnabled(LocationManager.GPS_PROVIDER) ||
                    lm.isProviderEnabled(LocationManager.NETWORK_PROVIDER)
            } catch (_: Throwable) {
                false
            }
        }
    }

    /**
     * True iff all three gates are satisfied — SSID reads should
     * work. False = at least one gate is closed; the Settings UI
     * shows WHICH.
     */
    fun ssidDetectionReady(context: Context): Boolean {
        return hasFineLocationPermission(context) &&
            hasNearbyWifiPermission(context) &&
            isLocationServicesEnabled(context)
    }

    /**
     * Intent that opens the system's per-app-permissions page
     * for THIS app — user can flip the Location toggle there
     * without hunting through System → Apps → ... manually.
     */
    fun openAppDetailsIntent(context: Context): Intent {
        return Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS).apply {
            data = Uri.fromParts("package", context.packageName, null)
            flags = Intent.FLAG_ACTIVITY_NEW_TASK
        }
    }

    /**
     * Intent that opens the system's Location settings page so
     * the user can flip the OS-wide Location toggle.
     */
    fun openLocationSettingsIntent(): Intent {
        return Intent(Settings.ACTION_LOCATION_SOURCE_SETTINGS).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK
        }
    }
}
