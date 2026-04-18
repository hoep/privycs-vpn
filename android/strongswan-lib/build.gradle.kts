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

    // Library modules don't own minification - that's the app module's job.
    // consumerProguardFiles in defaultConfig already declares our keep rules;
    // a separate buildTypes.release block with getDefaultProguardFile causes
    // AGP's extractProguardFiles -> mergeReleaseConsumerProguardFiles task
    // ordering to fail validation.

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

    // Same trick for resources: strip the strongSwan launcher/app/shortcut
    // mipmaps so OUR app's @mipmap/ic_launcher wins on every DPI, including
    // the adaptive-icon XML under mipmap-anydpi-v26 which otherwise takes
    // precedence over our raster PNGs on Android 8+.
    val strongswanResFiltered = layout.buildDirectory.dir("generated/strongswanRes")
    // Bump this string whenever the rebrand filter below changes so Gradle
    // considers the sync stale and reruns the copy. Declared as an input
    // property on the task, this is what breaks the silent build-cache hit
    // that kept restoring the unrebranded strings.xml across v0.9.1.2-.4.
    val rebrandVersion = "2"
    val syncStrongswanRes = tasks.register<Sync>("syncStrongswanRes") {
        inputs.property("rebrandVersion", rebrandVersion)
        // Belt to the previous braces: also always treat the output as out
        // of date. The input-property alone would be enough, but the user-
        // visible cost of a false positive (a few Gradle-task seconds) is
        // far below the cost of a silent brand regression.
        outputs.upToDateWhen { false }
        from("../vendor/strongswan/src/frontends/android/app/src/main/res") {
            exclude("mipmap-anydpi-v26/ic_launcher.xml")
            filesMatching("values/strings.xml") {
                filter { line ->
                    line
                        .replace(
                            "<string name=\"app_name\">strongSwan VPN Client</string>",
                            "<string name=\"app_name\">Privycs VPN</string>"
                        )
                        .replace(
                            "<string name=\"main_activity_name\">strongSwan</string>",
                            "<string name=\"main_activity_name\">Privycs VPN</string>"
                        )
                        .replace(
                            "<string name=\"strongswan_shortcut\">strongSwan shortcut</string>",
                            "<string name=\"strongswan_shortcut\">Privycs VPN shortcut</string>"
                        )
                        .replace(
                            "<string name=\"log_mail_subject\">strongSwan %1\$s Log File</string>",
                            "<string name=\"log_mail_subject\">Privycs VPN %1\$s Log File</string>"
                        )
                }
            }
        }
        into(strongswanResFiltered)
    }

    sourceSets {
        getByName("main") {
            java.srcDirs(
                "src/main/java",
                strongswanJavaFiltered
            )
            res.srcDirs(
                "src/main/res",
                strongswanResFiltered
            )
            manifest.srcFile("src/main/AndroidManifest.xml")
        }
    }

    // Hang both sync tasks off preBuild so they run before any
    // compile/merge/AAPT2 stage reads from the generated source dirs.
    // Blanket dependency is safer than trying to enumerate every task AGP
    // introduces per variant.
    tasks.matching { it.name == "preBuild" }.configureEach {
        dependsOn(syncStrongswanJava, syncStrongswanRes)
    }
    // Belt-and-braces: explicitly tie Kotlin + Java compile. Some AGP
    // versions skip preBuild when only compile tasks are requested.
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

    // The submodule ships layouts and resources for an entire app. Because we
    // drop the upstream ui/ Java package, lint flags every remaining layout
    // that referenced those classes (MissingClass) plus various i18n /
    // hardcoded-string / unused-resource findings that are out of our control.
    // Treat lint as advisory for this wrapper; :app's lint still runs over
    // our first-party code.
    lint {
        abortOnError = false
        checkReleaseBuilds = false
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
