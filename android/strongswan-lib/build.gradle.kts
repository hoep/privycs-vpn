// strongSwan library module.
//
// Wraps the Java + native code of strongSwan's upstream Android frontend
// (vendored at android/vendor/strongswan). Pulls Java sources and resources
// directly from the submodule via sourceSets so upstream updates flow in via
// `git submodule update` without local file duplication.
//
// Namespace is intentionally org.strongswan.android so CharonVpnService,
// VpnStateService, and the generated R class keep their upstream FQNs -
// required because libandroidbridge.so looks up Java classes via
// JNI FindClass("org/strongswan/android/...") at JNI_OnLoad time.
//
// The :app module depends on this library and calls into CharonVpnService
// via Intent, without leaking strongSwan types across the API boundary.

plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "org.strongswan.android"
    compileSdk = 34

    defaultConfig {
        minSdk = 26

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        consumerProguardFiles("consumer-rules.pro")

        ndk {
            abiFilters += listOf("arm64-v8a", "armeabi-v7a", "x86_64")
        }

        externalNativeBuild {
            ndkBuild {
                arguments += "-j${Runtime.getRuntime().availableProcessors()}"
                // Skip BYOD (TNC/IMC) native plugins for the first release -
                // we only need cert-based IKEv2 which works without them.
                // The corresponding Java classes still compile but are never
                // invoked on the cert-auth path.
                arguments += "strongswan_USE_BYOD="
            }
        }
    }

    ndkVersion = "27.3.13750724"

    externalNativeBuild {
        ndkBuild {
            path = file("../vendor/strongswan/src/frontends/android/app/src/main/jni/Android.mk")
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            consumerProguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "consumer-rules.pro"
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    // Pull Java + resources directly from the submodule. No local copy, no
    // patched fork - upstream is the single source of truth.
    sourceSets {
        getByName("main") {
            java.srcDirs(
                "../vendor/strongswan/src/frontends/android/app/src/main/java"
            )
            res.srcDirs(
                "../vendor/strongswan/src/frontends/android/app/src/main/res"
            )
            manifest.srcFile("src/main/AndroidManifest.xml")
        }
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1,DEPENDENCIES,LICENSE,NOTICE}"
        }
    }
}

dependencies {
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.core:core:1.13.1")
    implementation("androidx.lifecycle:lifecycle-process:2.8.7")
    implementation("androidx.preference:preference:1.2.1")
    implementation("androidx.localbroadcastmanager:localbroadcastmanager:1.1.0")
    implementation("com.google.android.material:material:1.12.0")
}
