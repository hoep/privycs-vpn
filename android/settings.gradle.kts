pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
        maven { url = uri("https://jitpack.io") }
    }
    // v0.9.15.x AmneziaWG: import the upstream submodule's
    // version catalog so its tunnel/build.gradle.kts can resolve
    // `alias(libs.plugins.android.library)` without us forking
    // the upstream Gradle config. Single catalog shared by the
    // amneziawg-tunnel module and (transitively) any of our own
    // future modules that want to reference these versions.
    versionCatalogs {
        create("libs") {
            from(files("vendor/amneziawg-android/gradle/libs.versions.toml"))
        }
    }
}

rootProject.name = "PrivycsVPN"
include(":app")
include(":strongswan-lib")
include(":openvpn-lib")

// AmneziaWG (AWG) — DPI-evasion fork of WireGuard. Same userspace
// Backend / Tunnel / Statistics API as wireguard-android, in
// package `org.amnezia.awg.*`. Pulled in as git submodule pinned
// to a tagged release. Stage 1 of AMNEZIAWG_CLIENT_PLAN.md.
include(":amneziawg-tunnel")
project(":amneziawg-tunnel").projectDir = file("vendor/amneziawg-android/tunnel")
