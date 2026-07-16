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

# SLF4J (pulled in by Ktor) -- not used at runtime on Android
-dontwarn org.slf4j.**
-keep class org.slf4j.** { *; }

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

# MaxMind MMDB reader uses reflection to instantiate decoded
# record types. Keep the public API + the Reader internals.
-keep class com.maxmind.db.** { *; }
-dontwarn com.maxmind.db.**

# BouncyCastle JCA provider -- required by P12Identity, which parses the gateway
# .sswan's PKCS#12 through KeyStore.getInstance("PKCS12", BouncyCastleProvider()).
#
# BouncyCastleProvider itself needs no rule: P12Identity references it directly, so R8
# both keeps it and renames it safely. Its CONTENTS are the problem. Registration runs
# through two string-resolved hops R8 cannot trace, and it fails SILENTLY -- BC's
# ClassUtil.loadClass swallows the ClassNotFoundException, so a stripped provider
# registers nothing and every gateway IPSec connect dies on KeyStore.getInstance.
# Debug builds and the JVM unit tests keep the full classpath and never see it.
#   1. the constructor reflects on the literal "org.bouncycastle.jcajce.provider.
#      <group>." + <Name> + "$Mappings";
#   2. each $Mappings registers its SPI as a class-NAME string, which the JCA then
#      resolves with Class.forName at getInstance time.
# Both hops resolve ORIGINAL names, so these must be -keep (never -keepnames): they
# must survive shrinking AND obfuscation. Inner classes are pinned with the outer
# because several $Mappings derive their SPI prefix from <Outer>.class.getName() --
# a renamed outer would look up an inner under its old name.
#
# Scoped to the algorithms the two encodings the gateway emits actually need
# (ipsec_pki_crypto.go:444-452), NOT org.bouncycastle.** -- bcprov ships 2407 classes
# under jcajce/provider and this APK ships to users. Each rule below was confirmed
# load-bearing by ablation: dropping any one fails a real p12 parse. Everything else BC
# touches (crypto engines, asn1, the SPI base classes) is reachable by direct reference
# and needs no rule.

# KeyStore.PKCS12 SPI -- the entry point both encodings go through.
-keep class org.bouncycastle.jcajce.provider.keystore.PKCS12 { *; }
-keep class org.bouncycastle.jcajce.provider.keystore.PKCS12$** { *; }
-keep class org.bouncycastle.jcajce.provider.keystore.pkcs12.** { *; }
-keep class org.bouncycastle.jcajce.provider.keystore.util.** { *; }

# Bag/key decryption. DESede = gopkcs12.LegacyDES (PBE-SHA1-3DES on key and cert bags);
# AES + PBEPBKDF2 = gopkcs12.Modern (PBES2/PBKDF2, AES-256-CBC).
-keep class org.bouncycastle.jcajce.provider.symmetric.AES { *; }
-keep class org.bouncycastle.jcajce.provider.symmetric.AES$** { *; }
-keep class org.bouncycastle.jcajce.provider.symmetric.DESede { *; }
-keep class org.bouncycastle.jcajce.provider.symmetric.DESede$** { *; }
-keep class org.bouncycastle.jcajce.provider.symmetric.PBEPBKDF2 { *; }
-keep class org.bouncycastle.jcajce.provider.symmetric.PBEPBKDF2$** { *; }

# PKCS#12 MAC verification -- the wrong-password signal. SHA-1 for LegacyDES,
# SHA-256 for Modern.
-keep class org.bouncycastle.jcajce.provider.digest.SHA1 { *; }
-keep class org.bouncycastle.jcajce.provider.digest.SHA1$** { *; }
-keep class org.bouncycastle.jcajce.provider.digest.SHA256 { *; }
-keep class org.bouncycastle.jcajce.provider.digest.SHA256$** { *; }

# Key reconstruction from the shrouded key bag (RSA-4096 / EC P-384 client certs) and
# the CertificateFactory the keystore builds its cert bags with.
-keep class org.bouncycastle.jcajce.provider.asymmetric.RSA { *; }
-keep class org.bouncycastle.jcajce.provider.asymmetric.RSA$** { *; }
-keep class org.bouncycastle.jcajce.provider.asymmetric.EC { *; }
-keep class org.bouncycastle.jcajce.provider.asymmetric.EC$** { *; }
-keep class org.bouncycastle.jcajce.provider.asymmetric.X509 { *; }
-keep class org.bouncycastle.jcajce.provider.asymmetric.X509$** { *; }
-keep class org.bouncycastle.jcajce.provider.asymmetric.x509.** { *; }
