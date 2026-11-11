// swift-tools-version:5.9
// The platform-independent logic of the macOS extensions, for `swift test`.
// The extensions themselves are built by build.sh (swiftc), not by SwiftPM.
import PackageDescription

let package = Package(
    name: "StrataOSMac",
    targets: [
        .target(name: "StrataOSCore", path: "Sources/StrataOSCore"),
        .testTarget(name: "StrataOSCoreTests", dependencies: ["StrataOSCore"], path: "Tests/StrataOSCoreTests"),
    ]
)
