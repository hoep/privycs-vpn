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

# Keep WireGuard tunnel library
-keep class com.wireguard.** { *; }

# Keep Ktor
-keep class io.ktor.** { *; }
-dontwarn io.ktor.**

# Keep OkHttp
-dontwarn okhttp3.**
-dontwarn okio.**
