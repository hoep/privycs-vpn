// swift-tools-version: 5.10
import PackageDescription

// Local SwiftPM wrapper around the Smart Decision Engine gomobile xcframework
// (engine/ffi), built by ios/Scripts/build-engine-xcframework.sh into
// Engine.xcframework alongside this manifest. The xcframework is gitignored and
// rebuilt in CI before xcodebuild. Consumed by the iOS App target only (the
// engine lives in the app, never the NetworkExtension).
let package = Package(
    name: "Engine",
    platforms: [
        .iOS(.v17),
        .tvOS(.v17),
    ],
    products: [
        .library(name: "Engine", targets: ["Engine"]),
    ],
    targets: [
        .binaryTarget(
            name: "Engine",
            path: "Engine.xcframework"
        ),
    ]
)
