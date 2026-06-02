// swift-tools-version: 5.10
// PrivycsCore — Shared business logic for the Privycs VPN iOS app + Network Extension.

import PackageDescription

let package = Package(
    name: "PrivycsCore",
    defaultLocalization: "en",
    platforms: [
        .iOS(.v17),
        .macOS(.v14), // optional: same framework reusable on macOS for parity
    ],
    products: [
        .library(
            name: "PrivycsCore",
            targets: ["PrivycsCore"]
        ),
    ],
    dependencies: [
        // Crypto for ed25519 license-key verify. Apple's swift-crypto
        // is the same API surface as CryptoKit but Linux-runnable, so
        // CI can do hard-tests of the verify path on Linux too.
        .package(url: "https://github.com/apple/swift-crypto.git", from: "3.0.0"),

        // SQLite ORM for pool member storage. Pool tables can have
        // 100+ members; a real DB is overkill for Settings but right
        // for that case. GRDB is the best-maintained Swift SQLite lib.
        .package(url: "https://github.com/groue/GRDB.swift.git", from: "6.29.0"),

        // Sentry for crash reporting → self-hosted Bugsink at
        // crashes.privycs.com. Default OFF, opt-in via Settings UI.
        // Sentry temporarily REMOVED for the iOS Xcode-26 alpha track.
        // Sentry 8.x binary xcframeworks are built with Swift 5.9 and
        // can't load under Xcode 26's Swift 6.3 compiler. Sentry 9.x
        // is Xcode-26-compatible but has breaking API changes
        // (Event.message, Exception.stacktrace etc.) — migration is
        // tracked separately. CrashReporter.swift has `#if
        // canImport(Sentry)` guards, so removing the dep is inert.
        // .package(url: "https://github.com/getsentry/sentry-cocoa.git", from: "9.15.0"),

        // Apple swift-collections for OrderedSet (used in PoolRotator).
        .package(url: "https://github.com/apple/swift-collections.git", from: "1.1.0"),

        // ZIPFoundation for bulk pool import from provider .zip archives
        // (Android PoolImporter parity). Pure-Swift + zlib, Linux-buildable
        // so PoolImporter stays unit-testable.
        .package(url: "https://github.com/weichsel/ZIPFoundation.git", from: "0.9.0"),
    ],
    targets: [
        .target(
            name: "PrivycsCore",
            dependencies: [
                .product(name: "Crypto", package: "swift-crypto"),
                .product(name: "GRDB", package: "GRDB.swift"),
                // .product(name: "Sentry", package: "sentry-cocoa"),  // see comment above
                .product(name: "Collections", package: "swift-collections"),
                .product(name: "ZIPFoundation", package: "ZIPFoundation"),
            ],
            path: "Sources/PrivycsCore",
            resources: [
                // IP→country DB (db-ip "IP to Country Lite", CC BY 4.0) —
                // the same file Android bundles, read by MmdbCountryResolver
                // to resolve pool/server country codes → flags.
                .copy("Resources/country.mmdb"),
                .copy("Resources/country.mmdb.LICENSE"),
            ]
        ),
        .testTarget(
            name: "PrivycsCoreTests",
            dependencies: ["PrivycsCore"],
            path: "Tests/PrivycsCoreTests"
        ),
    ]
)
