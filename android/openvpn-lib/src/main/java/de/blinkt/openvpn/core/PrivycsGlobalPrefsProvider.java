// Copyright (c) 2026 Privycs
// Distributed under the terms of the containing application's license.
package de.blinkt.openvpn.core;

import android.content.ContentProvider;
import android.content.ContentValues;
import android.database.Cursor;
import android.net.Uri;
import android.util.Log;

/**
 * Redundant seed for GlobalPreferences.instance.
 *
 * PrivycsApp.attachBaseContext already seeds this in every process, but on
 * some Samsung One UI builds the Application.onCreate appears to race the
 * foreground-service onStartCommand path: a ForegroundService can receive
 * onStartCommand before Application.onCreate finishes, producing the
 * "Global preferences instance is not set" crash at
 * OpenVPNService.java:549 -> GlobalPreferences.getInstance.
 *
 * Declaring this provider with android:process=":openvpn" forces AMS to
 * instantiate it during `:openvpn` subprocess init. ContentProvider.onCreate
 * is guaranteed to run BEFORE Application.onCreate completes AND before any
 * service in that process receives a lifecycle callback. Same seed call, just
 * from the most deterministically-early hook Android exposes.
 */
public class PrivycsGlobalPrefsProvider extends ContentProvider {

    @Override
    public boolean onCreate() {
        Context ctx = getContext();
        try {
            if (GlobalPreferences.instance == null) {
                GlobalPreferences.setInstance(false, false, false);
                Log.i("PrivycsPrefsProvider",
                    "ContentProvider seeded GlobalPreferences (pid=" + android.os.Process.myPid() + ")");
            }
            // Pre-load ProfileManager so any ProfileManager.get() call
            // from THIS process (main OR any subprocess Android's vpn
            // framework might decide to spawn despite our manifest not
            // declaring one) returns the persisted profile on the first
            // try instead of looping 100x on a stale SharedPreferences
            // cache. ContentProvider.onCreate runs before Application
            // .onCreate AND before any Service.onCreate in the same
            // process, so ProfileManager is warm by the time
            // OpenVPNService needs it.
            if (ctx != null) {
                ProfileManager.getInstance(ctx);
                Log.i("PrivycsPrefsProvider",
                    "ProfileManager warm-loaded (pid=" + android.os.Process.myPid() + ")");
            }
        } catch (Throwable t) {
            Log.e("PrivycsPrefsProvider", "seed failed", t);
        }
        return true;
    }

    @Override
    public Cursor query(Uri uri, String[] projection, String selection,
                        String[] selectionArgs, String sortOrder) {
        return null;
    }

    @Override
    public String getType(Uri uri) {
        return null;
    }

    @Override
    public Uri insert(Uri uri, ContentValues values) {
        return null;
    }

    @Override
    public int delete(Uri uri, String selection, String[] selectionArgs) {
        return 0;
    }

    @Override
    public int update(Uri uri, ContentValues values, String selection,
                      String[] selectionArgs) {
        return 0;
    }
}
