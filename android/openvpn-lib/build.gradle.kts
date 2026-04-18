// ics-openvpn library module.
//
// Wraps the Java + native code of schwabe/ics-openvpn (vendored at
// android/vendor/ics-openvpn) and exposes it as an android.library so the
// app module can depend on it. Upstream ships `main/` as an
// android.application with flavor dimensions (ui/skeleton x ovpn2/ovpn23),
// SWIG codegen, and compileSdk=36 / ndkVersion=29 - none of which
// match our app. Rather than patch the submodule, this wrapper re-expresses
// the same sources as a library with our toolchain pins.
//
// Namespace is intentionally de.blinkt.openvpn so OpenVPNService,
// VpnProfile, ConfigParser and the generated BuildConfig/R classes keep
// their upstream FQNs - required because libopenvpn.so and libovpnutil.so
// look up Java classes via JNI FindClass("de/blinkt/openvpn/...") at
// JNI_OnLoad time.
//
// Sources we pull from the submodule:
//   - main/src/main/java         - all core classes (OpenVPNService,
//                                   OpenVPNThread, OpenVpnManagementThread,
//                                   VpnProfile, ConfigParser, VpnStatus...)
//   - main/src/skeleton/java     - stubs (NotImplemented, ProfileEncryption,
//                                   VariantConfig) referenced by main/
//   - main/src/main/res          - strings, layouts, drawables, xml resources
//   - main/src/skeleton/res      - flavor-specific values (attrs.xml, styles)
//   - main/src/main/aidl         - AIDL for IOpenVPNServiceInternal /
//                                   IOpenVPNStatusCallback / IOpenVPNAPIService
//   - main/src/main/cpp          - C++ via CMake (openssl, openvpn 2.x, fmt,
//                                   lzo, lz4, PIE minivpn executable)
//
// What we intentionally do NOT wire up:
//   - The `ui` flavor (activities/fragments, MPAndroidChart, viewpager2)
//     - we ship our own Compose UI on top.
//   - ICSOpenVPNApplication - our PrivycsApp already owns the Application
//     class; the work it does (status listener init + notification channels)
//     is replicated in PrivycsApp.onCreate().
//   - The upstream manifest - AndroidManifest.xml below only declares the
//     two services and one activity we actually need; the rest of upstream's
//     manifest (LaunchVPN, DisconnectVPN, RemoteAction, ExternalOpenVPNService,
//     OnBootReceiver, keepVPNAlive...) is dropped.
//
// The :app module depends on this library and calls into OpenVPNService
// via VPNLaunchHelper.startOpenVpn() (a static entry that spins up the
// service with a VpnProfile).

plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "de.blinkt.openvpn"
    compileSdk = 34

    defaultConfig {
        minSdk = 26

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        consumerProguardFiles("consumer-rules.pro")

        // BuildConfig fields the vendor code reads at runtime. We pick the
        // ovpn2 flavor (classic C OpenVPN 2.x), so openvpn3 is false. The
        // vendor CMake still compiles the OpenVPN 3 C++ client + SWIG-generated
        // Java bindings (~5 MB extra per ABI) - we simply never invoke that
        // code path. Skipping it requires patching vendor CMakeLists.txt;
        // shipping both keeps the submodule untouched and simplifies updates.
        buildConfigField("boolean", "openvpn3", "false")

        // Upstream uses two flavor dimensions (ui/skeleton x ovpn2/ovpn23) so
        // AGP synthesises BuildConfig.FLAVOR at compile time. We ship no
        // flavors at all, so BuildConfig.FLAVOR does not exist and vendor
        // code referencing it (NativeUtils.java:66,
        // StatusListener.java:114) fails to compile with "cannot find
        // symbol: variable FLAVOR". Synthesise the field manually as a
        // constant "skeleton" string - that is the minimal-UI flavor upstream
        // uses for non-app library consumers, so the guarded code paths
        // (NativeUtils library-loading variant, StatusListener test-mode
        // shortcut) behave as if built as the skeleton flavor. Both call
        // sites only check .equals("skeleton"); the actual value just needs
        // to be a stable string.
        buildConfigField("String", "FLAVOR", "\"skeleton\"")

        ndk {
            abiFilters += listOf("arm64-v8a", "armeabi-v7a", "x86_64")
        }

        externalNativeBuild {
            cmake {
                // NOTE: Do NOT pass `-jN` here. That is an ndk-build (Make)
                // argument; CMake's configure phase rejects it with
                // "Unknown argument -j3" and the whole build aborts before
                // a single .cpp file is compiled. Ninja (the generator AGP
                // uses for CMake) auto-parallelises to nproc at build time,
                // so no explicit job count is needed.
                //
                // C++23 matches the `set(CMAKE_CXX_STANDARD 23)` in the
                // vendor CMakeLists. Specifying it here ensures AGP's own
                // default (C++14 or whatever ships with the NDK) does not
                // silently override it.
                cppFlags += "-std=c++23"
                arguments += "-DANDROID_STL=c++_static"

                // NDK 27's bionic libc declares basename/dirname/daemon/
                // gettimeofday/strsep in its sysroot headers; openvpn's own
                // src/compat/compat.h redeclares them with incompatible
                // signatures (non-const char*), so the openvpn .c files
                // fail to build with "conflicting types for 'basename'".
                // Upstream OpenVPN uses autoconf's HAVE_* probes to skip
                // the redeclarations when the platform already provides
                // them - but ics-openvpn's CMake build skips that autoconf
                // dance entirely and assumes every platform needs the
                // compat shims. Telling CMake that all five symbols ARE
                // present routes openvpn around its own shims, matching
                // what the equivalent strongSwan-lib fix does for
                // HAVE_SIGWAITINFO / HAVE_GETPWNAM_R etc.
                cFlags += listOf(
                    "-DHAVE_BASENAME",
                    "-DHAVE_DIRNAME",
                    "-DHAVE_DAEMON",
                    "-DHAVE_GETTIMEOFDAY",
                    "-DHAVE_STRSEP"
                )
            }
        }
    }

    // NDK 27.3 is what strongswan-lib already pins. Keeping both native
    // modules on the same NDK avoids pulling two NDK installs via sdkmanager
    // and keeps libc++ runtime linkage consistent across .so boundaries.
    ndkVersion = "27.3.13750724"

    externalNativeBuild {
        cmake {
            // Point straight at the submodule's CMakeLists. CMake expands
            // `../../../build/ovpnassets` inside the vendor's CMakeLists.txt
            // to vendor/ics-openvpn/main/build/ovpnassets - we mirror that
            // path below in sourceSets.main.assets.srcDirs so the PIE
            // `pie_openvpn.<abi>` binary gets packaged into the APK.
            path = file("../vendor/ics-openvpn/main/src/main/cpp/CMakeLists.txt")
            // Pin a CMake version that supports both the vendor's
            // cmake_minimum_required(3.4.1) and our CXX_STANDARD 23 request.
            // AGP 8.x's bundled 3.22.1 works fine.
            version = "3.22.1"
        }
    }

    buildFeatures {
        aidl = true         // AIDL interfaces: IOpenVPNServiceInternal,
                            // IOpenVPNStatusCallback, IOpenVPNAPIService
        buildConfig = true  // BuildConfig.openvpn3 is read by VpnProfile at runtime
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    sourceSets {
        getByName("main") {
            // Skeleton flavor sources provide NotImplemented / ProfileEncryption
            // / VariantConfig stubs that main/ references - without them the
            // full Java tree does not compile.
            //
            // `src/main/java` (this module's own sources) holds small bridge
            // classes in the de.blinkt.openvpn.* package - same package as the
            // vendor code, so they can call package-private APIs our app
            // module cannot reach (notably StatusListener.init).
            java.srcDirs(
                "src/main/java",
                "../vendor/ics-openvpn/main/src/main/java",
                "../vendor/ics-openvpn/main/src/skeleton/java"
            )
            res.srcDirs(
                "../vendor/ics-openvpn/main/src/main/res",
                "../vendor/ics-openvpn/main/src/skeleton/res"
            )
            aidl.srcDirs("../vendor/ics-openvpn/main/src/main/aidl")
            // CMake's post-build custom command copies pie_openvpn.<abi>
            // into vendor/ics-openvpn/main/build/ovpnassets. AGP's merge
            // assets task picks them up from here at packaging time.
            assets.srcDirs(
                "../vendor/ics-openvpn/main/src/main/assets",
                "../vendor/ics-openvpn/main/build/ovpnassets"
            )
            manifest.srcFile("src/main/AndroidManifest.xml")
        }
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1,DEPENDENCIES,LICENSE,NOTICE}"
        }
    }

    // Vendor sources ship with layouts and resources targeting features we
    // never invoke (UI flavor dialogs, external-API configs). Lint flags
    // them as MissingClass / UnusedResources which are not actionable from
    // our wrapper. Treat lint as advisory here; :app's lint still runs
    // over our own first-party code.
    lint {
        abortOnError = false
        checkReleaseBuilds = false
    }
}

dependencies {
    implementation("androidx.annotation:annotation:1.7.0")
    implementation("androidx.core:core-ktx:1.12.0")
    // LocaleHelper + Preferences use these. Kept minimal - NOT adding the
    // full androidx.appcompat / viewpager2 / MPAndroidChart stack from
    // upstream's ui flavor, since we don't link any of those classes.
}
