plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.serialization")
}

android {
    namespace = "com.privycs.vpn"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.privycs.vpn"
        minSdk = 26
        targetSdk = 34
        versionCode = 10011
        versionName = "0.9.11.43"

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        vectorDrawables {
            useSupportLibrary = true
        }

        ndk {
            // Must match the ABI filter in strongswan-lib so both modules
            // contribute native libs for the same architectures.
            abiFilters += listOf("arm64-v8a", "armeabi-v7a", "x86_64")
        }
    }

    signingConfigs {
        create("release") {
            val keystoreFile = System.getenv("ANDROID_KEYSTORE_PATH")
                ?: rootProject.file("app/upload.keystore").absolutePath
            val storePass = System.getenv("ANDROID_KEYSTORE_PASSWORD")
            val alias = System.getenv("ANDROID_KEY_ALIAS")
            val keyPass = System.getenv("ANDROID_KEY_PASSWORD")
            if (storePass != null && alias != null && keyPass != null && file(keystoreFile).exists()) {
                storeFile = file(keystoreFile)
                storePassword = storePass
                keyAlias = alias
                keyPassword = keyPass
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
            signingConfig = signingConfigs.getByName("release")
        }
        debug {
            isDebuggable = true
            applicationIdSuffix = ".debug"
        }
    }

    // APK output naming: produce "privycs-vpn-<version>.apk" for release
    // and "privycs-vpn-<version>-debug.apk" for debug. The Gradle default
    // ("app-release.apk" / "app-debug.apk") is fine for local module
    // output but useless for distribution - users download the file and
    // see a generic name with no app identity. Industry pattern (Signal,
    // Telegram, Bitwarden) is <app>-<version>[-variant].apk.
    //
    // Asymmetric suffix: release has none (it is the implicit default);
    // debug carries "-debug" because it is a meaningfully different
    // artifact (debuggable=true, no minification, .debug applicationId).
    applicationVariants.all {
        val variantName = this.buildType.name
        val versionStr = this.versionName
        outputs.all {
            val suffix = if (variantName == "debug") "-debug" else ""
            (this as com.android.build.gradle.internal.api.BaseVariantOutputImpl)
                .outputFileName = "privycs-vpn-${versionStr}${suffix}.apk"
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
        // Expose versionName / versionCode from build.gradle.kts as
        // BuildConfig.VERSION_NAME / VERSION_CODE so the About screen
        // reads them live instead of hardcoding "0.1.0".
        buildConfig = true
    }

    composeOptions {
        kotlinCompilerExtensionVersion = "1.5.8"
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
        // OpenVPN ships its CLI as a PIE executable renamed to
        // libovpnexec.so (add_executable in CMakeLists.txt). At runtime
        // OpenVPNThread.startOpenVPNThreadArgs calls ProcessBuilder.start
        // on {nativeLibraryDir}/libovpnexec.so. AGP 8's default
        // useLegacyPackaging=false keeps native libs uncompressed-in-APK
        // and NEVER extracts them to /data/app/<pkg>/lib/<abi>/ - great
        // for System.loadLibrary (linker can mmap from APK) but fatal for
        // ProcessBuilder because the kernel's execve() needs a real
        // filesystem path. Upstream ics-openvpn sets legacy packaging
        // specifically for this reason; we must do the same or OpenVPN
        // fails with "error=2, No such file or directory" on every
        // connect attempt.
        jniLibs {
            useLegacyPackaging = true
        }
    }
}

dependencies {
    // Compose BOM
    // 2024.02.00 (Compose UI 1.6.0) had a Samsung-specific crash in
    // AndroidComposeView.sendHoverExitEvent ("The ACTION_HOVER_EXIT
    // event was not cleared.") - Samsung devices synthesise hover
    // events that compose 1.6.0 did not clean up; the bug was fixed in
    // Compose UI 1.6.3 (BOM 2024.04.00). Bumping past that to
    // 2024.09.00 (Compose UI 1.7.3) for additional stability.
    val composeBom = platform("androidx.compose:compose-bom:2024.09.00")
    implementation(composeBom)

    // Compose
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-graphics")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")

    // Navigation
    implementation("androidx.navigation:navigation-compose:2.7.7")

    // Activity Compose
    implementation("androidx.activity:activity-compose:1.8.2")

    // Lifecycle
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.7.0")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.7.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.7.0")

    // Core KTX
    implementation("androidx.core:core-ktx:1.12.0")

    // WireGuard tunnel library
    implementation("com.wireguard.android:tunnel:1.0.20230706")

    // OpenVPN via ics-openvpn. The library module wraps schwabe/ics-openvpn
    // at android/vendor/ics-openvpn (pinned to v0.7.64). See
    // android/openvpn-lib/build.gradle.kts for the wrapper layout.
    // CMake builds OpenSSL, OpenVPN 2.x, lzo, lz4, fmt and the PIE minivpn
    // executable. Swig + cmake must be on PATH at build time (handled in
    // the android-build.yml / android-release.yml CI workflows and in
    // scripts/prepare-openvpn.sh for local builds).
    implementation(project(":openvpn-lib"))

    // IPSec/IKEv2 via strongSwan libcharon. The library module wraps
    // strongSwan's upstream Android frontend (Java + native) at
    // android/vendor/strongswan. scripts/prepare-strongswan.sh must run
    // before a clean build to produce Android.common.mk + libcrypto_static.
    implementation(project(":strongswan-lib"))

    // Ktor client
    implementation("io.ktor:ktor-client-core:2.3.8")
    implementation("io.ktor:ktor-client-okhttp:2.3.8")
    implementation("io.ktor:ktor-client-content-negotiation:2.3.8")
    implementation("io.ktor:ktor-serialization-kotlinx-json:2.3.8")

    // Kotlin serialization
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.6.2")

    // Coroutines
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.7.3")

    // DataStore Preferences
    implementation("androidx.datastore:datastore-preferences:1.0.0")

    // QR code scanner via Google Play Services Code Scanner.
    // Uses Google Play Services' own camera UI in a separate process,
    // so we do NOT need to declare the CAMERA permission in the
    // manifest and users are not prompted for camera access by our
    // app - GMS handles the UX itself. Replaces the alternatives of
    // ML Kit Barcode Scanning (needs manual CameraX + camera perm) and
    // ZXing (older, more boilerplate).
    implementation("com.google.android.gms:play-services-code-scanner:16.1.0")

    // MMDB reader for Pool feature's country-of-origin lookup. We
    // use the lightweight maxmind-db library (NOT geoip2) - it just
    // reads the binary MMDB format without the GeoIP2-specific
    // model classes. ~250 KB. Both MaxMind GeoLite2-Country and
    // db-ip's CC-BY-licensed dbip-country-lite are in MMDB format
    // so this reader works with either.
    implementation("com.maxmind.db:maxmind-db:3.1.0")

    // Debug
    debugImplementation("androidx.compose.ui:ui-tooling")
    debugImplementation("androidx.compose.ui:ui-test-manifest")

    // Test
    testImplementation("junit:junit:4.13.2")
    androidTestImplementation("androidx.test.ext:junit:1.1.5")
    androidTestImplementation("androidx.test.espresso:espresso-core:3.5.1")
    androidTestImplementation(composeBom)
    androidTestImplementation("androidx.compose.ui:ui-test-junit4")
}
