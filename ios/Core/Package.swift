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
        // Sentry 8.57.0+ is the first version built with Xcode 26 /
        // Swift 6 so Xcode-26 archive doesn't fail with
        // "this SDK is not supported by the compiler" on the older
        // Sentry framework binaries.
        .package(url: "https://github.com/getsentry/sentry-cocoa.git", from: "8.57.0"),

        // Apple swift-collections for OrderedSet (used in PoolRotator).
        .package(url: "https://github.com/apple/swift-collections.git", from: "1.1.0"),
    ],
    targets: [
        .target(
            name: "PrivycsCore",
            dependencies: [
                .product(name: "Crypto", package: "swift-crypto"),
                .product(name: "GRDB", package: "GRDB.swift"),
                .product(name: "Sentry", package: "sentry-cocoa"),
                .product(name: "Collections", package: "swift-collections"),
            ],
            path: "Sources/PrivycsCore",
            resources: [
                // Localizable.strings × 6 — gepflegt via String Catalog (.xcstrings)
                // im App-Target, hier nur fallback default.
            ]
        ),
        .testTarget(
            name: "PrivycsCoreTests",
            dependencies: ["PrivycsCore"],
            path: "Tests/PrivycsCoreTests"
        ),
    ]
)
