# Privycs VPN ProGuard Rules

# Keep kotlinx.serialization
-keepattributes *Annotation*, InnerClasses
-dontnote kotlinx.serialization.AnnotationsKt

-keepclassmembers class kotlinx.serialization.json.** {
    *** Companion;
}
-keepclasseswithmembers class kotlinx.serialization.json.** {
    kotlinx.serialization.KSerializer serializer(...);
}
-keep,includedescriptorclasses class com.privycs.vpn.data.models.**$$serializer { *; }
-keepclassmembers class com.privycs.vpn.data.models.** {
    *** Companion;
}
-keepclasseswithmembers class com.privycs.vpn.data.models.** {
    kotlinx.serialization.KSerializer serializer(...);
}

# Keep IpSecTunnel serializable config models
-keep,includedescriptorclasses class com.privycs.vpn.service.IpSecTunnel$*$$serializer { *; }
-keepclassmembers class com.privycs.vpn.service.IpSecTunnel$* {
    *** Companion;
}
-keepclasseswithmembers class com.privycs.vpn.service.IpSecTunnel$* {
    kotlinx.serialization.KSerializer serializer(...);
}

# Keep WireGuard tunnel library
-keep class com.wireguard.** { *; }

# Keep Ktor
-keep class io.ktor.** { *; }
-dontwarn io.ktor.**

# Keep OkHttp
-dontwarn okhttp3.**
-dontwarn okio.**

# Keep Ktor serialization converters
-keep class io.ktor.serialization.** { *; }

# Keep coroutines internal classes
-dontwarn kotlinx.coroutines.**
-keep class kotlinx.coroutines.** { *; }

# Keep VPN service classes (accessed via Intent reflection)
-keep class com.privycs.vpn.service.PrivycsVpnService { *; }
-keep class com.privycs.vpn.service.WireGuardTunnel { *; }
-keep class com.privycs.vpn.service.OpenVpnTunnel { *; }
-keep class com.privycs.vpn.service.IpSecTunnel { *; }

# Keep Android IKEv2 API classes (API 31+)
-keep class android.net.ipsec.ike.** { *; }
-dontwarn android.net.ipsec.ike.**

# Keep strongSwan classes (when integrated)
-keep class org.strongswan.** { *; }
-dontwarn org.strongswan.**

# Keep OpenVPN classes (when ics-openvpn is integrated)
-keep class de.blinkt.openvpn.** { *; }
-dontwarn de.blinkt.openvpn.**
