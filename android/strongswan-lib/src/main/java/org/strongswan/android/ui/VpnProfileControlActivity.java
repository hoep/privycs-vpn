/*
 * Stub for the strongSwan upstream VpnProfileControlActivity.
 *
 * CharonVpnService + VpnStateService reference this class for intent actions
 * and extras (START_PROFILE, DISCONNECT, EXTRA_VPN_PROFILE_UUID). The
 * constant string values here match upstream exactly so the runtime action
 * strings stay compatible; only the Activity body is dropped. See the
 * companion MainActivity stub for the broader rationale.
 */
package org.strongswan.android.ui;

import android.app.Activity;

public class VpnProfileControlActivity extends Activity {
    public static final String START_PROFILE = "org.strongswan.android.action.START_PROFILE";
    public static final String DISCONNECT = "org.strongswan.android.action.DISCONNECT";
    public static final String EXTRA_VPN_PROFILE_UUID = "org.strongswan.android.VPN_PROFILE_UUID";
}
