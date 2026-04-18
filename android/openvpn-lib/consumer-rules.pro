# Keep the classes JNI (libopenvpn.so / libovpnutil.so) look up by name
# via FindClass at runtime. R8 would otherwise rename/drop these and the
# native side would crash with NoSuchMethodError on the first VPN connect.
-keep class de.blinkt.openvpn.core.** { *; }
-keep class de.blinkt.openvpn.VpnProfile { *; }
-keep class de.blinkt.openvpn.api.** { *; }

# Keep AIDL-generated stubs / proxies intact.
-keep class de.blinkt.openvpn.core.IOpenVPNServiceInternal { *; }
-keep class de.blinkt.openvpn.core.IOpenVPNServiceInternal$* { *; }
-keep class de.blinkt.openvpn.core.IStatusCallbacks { *; }
-keep class de.blinkt.openvpn.core.IStatusCallbacks$* { *; }
-keep class de.blinkt.openvpn.api.IOpenVPNAPIService { *; }
-keep class de.blinkt.openvpn.api.IOpenVPNAPIService$* { *; }

# VpnProfile is serialized to SharedPreferences via ObjectOutputStream - R8
# stripping its fields would break load on upgrade.
-keepclassmembers class de.blinkt.openvpn.VpnProfile {
    <fields>;
}
-keepclassmembers class de.blinkt.openvpn.core.ConnectionStatus { *; }
-keepclassmembers class de.blinkt.openvpn.core.Connection { <fields>; }
