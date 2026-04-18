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
                // NDK 27 sysroot headers declare these since API 23/21 and our
                // minSdk is 26, so strongSwan's compat shims (guarded by
                // `#if !defined(HAVE_*)`) collide with the NDK declarations.
                // Defining HAVE_* disables the shims and routes calls straight
                // to bionic/clang intrinsics.
                arguments += "APP_CFLAGS=" +
                        "-DHAVE_SIGWAITINFO " +
                        "-DHAVE_GETPWNAM_R " +
                        "-DHAVE_GETGRNAM_R " +
                        "-DHAVE_GETPWUID_R " +
                        "-DHAVE_GCC_ATOMIC_OPERATIONS"
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
                "../vendor/strongswan/src/frontends/android/app/src/main/java",
                // Minimal Activity stubs at the org.strongswan.android.ui FQN
                // so CharonVpnService + VpnStateService compile without
                // pulling in the upstream UI tree (the UI calls
                // WindowCompat.enableEdgeToEdge which needs androidx.core
                // 1.15+, which in turn needs compileSdk 35 - out of scope
                // for this release).
                "src/main/java"
            )
            // Drop the upstream UI so we don't inherit its resource deps.
            // Our :app owns the UI anyway, the strongSwan Activities would
            // never be reachable.
            java.exclude("org/strongswan/android/ui/**")
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
