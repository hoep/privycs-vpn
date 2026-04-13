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
        versionCode = 1
        versionName = "0.1.0"

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        vectorDrawables {
            useSupportLibrary = true
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
        }
        debug {
            isDebuggable = true
            applicationIdSuffix = ".debug"
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
    }

    composeOptions {
        kotlinCompilerExtensionVersion = "1.5.8"
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
    }
}

dependencies {
    // Compose BOM
    val composeBom = platform("androidx.compose:compose-bom:2024.02.00")
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

    // OpenVPN integration
    // The ics-openvpn library is not published to Maven Central or JitPack as a
    // standalone artifact. To integrate full OpenVPN support, choose one of:
    //
    // Option A: Clone ics-openvpn as a Git submodule and include as a local module:
    //   git submodule add https://github.com/nickolay/ics-openvpn.git libs/ics-openvpn
    //   In settings.gradle.kts: include(":libs:ics-openvpn:main")
    //   Then: implementation(project(":libs:ics-openvpn:main"))
    //
    // Option B: Build the AAR from ics-openvpn and add as a local file dependency:
    //   implementation(files("libs/openvpn-core.aar"))
    //
    // The current OpenVpnTunnel.kt uses Android VpnService directly and can be
    // enhanced with either approach without changing the public API.

    // IPSec/IKEv2 integration
    // For API 31+ (Android 12): Uses built-in android.net.ipsec.ike APIs (no extra dep).
    // For API 26-30: strongSwan integration is recommended.
    //
    // To add strongSwan support:
    //   git submodule add https://github.com/nickolay/strongswan-android.git libs/strongswan
    //   implementation(project(":libs:strongswan:app"))
    //
    // Or build the AAR and include as a file dependency:
    //   implementation(files("libs/libcharon.aar"))

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
