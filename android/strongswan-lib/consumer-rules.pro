# Consumer ProGuard rules for strongswan-lib.
#
# libandroidbridge.so uses JNI FindClass to look up specific Java classes
# and FindClass uses their binary names, so minification must not rename
# them or their native-method signatures.

-keep class org.strongswan.android.logic.CharonVpnService { *; }
-keep class org.strongswan.android.logic.CharonVpnService$* { *; }
-keep class org.strongswan.android.logic.SimpleFetcher { *; }
-keep class org.strongswan.android.logic.NetworkManager { *; }
-keep class org.strongswan.android.logic.Scheduler { *; }
-keep class org.strongswan.android.logic.VpnStateService { *; }
-keep class org.strongswan.android.logic.VpnStateService$* { *; }
-keep class org.strongswan.android.logic.imc.** { *; }
-keep class org.strongswan.android.data.VpnProfile { *; }
-keep class org.strongswan.android.data.VpnProfile$* { *; }
-keep class org.strongswan.android.data.VpnType { *; }
-keep class org.strongswan.android.data.VpnType$* { *; }
-keepclasseswithmembernames class * {
    native <methods>;
}
