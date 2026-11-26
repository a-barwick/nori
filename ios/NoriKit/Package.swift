// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "NoriKit",
    platforms: [
        .iOS(.v18),
        .macOS(.v15)
    ],
    products: [
        .library(name: "NoriCore", targets: ["NoriCore"]),
        .library(name: "NoriDesign", targets: ["NoriDesign"]),
        .library(name: "NoriFeatures", targets: ["NoriFeatures"])
    ],
    targets: [
        .target(
            name: "NoriCore",
            dependencies: [],
            path: "Sources/NoriCore"
        ),
        .target(
            name: "NoriDesign",
            dependencies: ["NoriCore"],
            path: "Sources/NoriDesign"
        ),
        .target(
            name: "NoriFeatures",
            dependencies: ["NoriCore", "NoriDesign"],
            path: "Sources/NoriFeatures"
        ),
        .testTarget(
            name: "NoriCoreTests",
            dependencies: ["NoriCore"],
            path: "Tests/NoriCoreTests"
        ),
        .testTarget(
            name: "NoriDesignTests",
            dependencies: ["NoriDesign"],
            path: "Tests/NoriDesignTests"
        ),
        .testTarget(
            name: "NoriFeaturesTests",
            dependencies: ["NoriFeatures", "NoriCore", "NoriDesign"],
            path: "Tests/NoriFeaturesTests"
        )
    ]
)
