/*
 * Stub for the strongSwan upstream MainActivity.
 *
 * CharonVpnService imports org.strongswan.android.ui.MainActivity to build
 * PendingIntents for its foreground notification. We do not ship the
 * upstream UI (the :app module owns the UI and the library's upstream
 * Activities pull in deps that require compileSdk 35+), so this minimal
 * subclass exists only to satisfy the import and the Intent class-reference.
 *
 * It is deliberately not registered in the manifest - the PendingIntents
 * built against it simply resolve to no receiver at runtime, which is the
 * same no-op behavior the embedding app gets anyway.
 */
package org.strongswan.android.ui;

import android.app.Activity;

public class MainActivity extends Activity {
}
