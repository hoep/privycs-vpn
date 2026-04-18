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

    // Pull Java + resources from the submodule. The upstream UI package
    // (org/strongswan/android/ui/**) cannot be included as-is:
    //   - Activities call WindowCompat.enableEdgeToEdge(Window), added in
    //     androidx.core 1.15.0 which requires compileSdk 35 (we are on 34).
    //   - Several classes use `switch (R.id.xxx)`, rejected by AGP in
    //     library modules because library R fields are non-final
    //     ("constant expression required").
    //
    // So we sync the submodule's java tree into the build dir with the ui/
    // subtree filtered out, and provide our own minimal Activity stubs at
    // src/main/java/org/strongswan/android/ui/ for the two classes that
    // CharonVpnService + VpnStateService import by FQN.
    //
    // Using a Sync task (not a SourceDirectorySet.exclude) so the filter
    // only applies to the submodule copy; our local stub srcDir stays
    // untouched.
    val strongswanJavaFiltered = layout.buildDirectory.dir("generated/strongswanJava")
    val syncStrongswanJava = tasks.register<Sync>("syncStrongswanJava") {
        from("../vendor/strongswan/src/frontends/android/app/src/main/java") {
            exclude("org/strongswan/android/ui/**")
        }
        into(strongswanJavaFiltered)
    }

    sourceSets {
        getByName("main") {
            java.srcDirs(
                "src/main/java",
                strongswanJavaFiltered
            )
            res.srcDirs(
                "../vendor/strongswan/src/frontends/android/app/src/main/res"
            )
            manifest.srcFile("src/main/AndroidManifest.xml")
        }
    }

    // Make every Java-compile step wait for the filtered sync. Covers Debug
    // and Release variants without naming them explicitly.
    tasks.withType<org.jetbrains.kotlin.gradle.tasks.KotlinCompile>().configureEach {
        dependsOn(syncStrongswanJava)
    }
    tasks.matching { it.name.startsWith("compile") && it.name.endsWith("JavaWithJavac") }
        .configureEach { dependsOn(syncStrongswanJava) }

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
