import Foundation

public typealias HomeConnectorModel = OpenAPI.HomeConnectorModel
public typealias HomeConnector = OpenAPI.HomeConnector
public typealias HomeConnectorDownloads = OpenAPI.HomeConnectorDownloads
public typealias HomeConnectorsResponse = OpenAPI.HomeConnectors
public typealias HomePairing = OpenAPI.HomePairing
public typealias HomePairingRequest = OpenAPI.HomePairingRequest
public typealias HomeConnectorTest = OpenAPI.HomeConnectorTestResponse

extension OpenAPI.HomeConnector: Identifiable {}

extension OpenAPI.HomeConnectorDownloads {
    public var macOS: String? {
        get { macos }
        set { macos = newValue }
    }

    public var linuxAMD64: String? {
        get { linuxAmd64 }
        set { linuxAmd64 = newValue }
    }

    public var linuxARM64: String? {
        get { linuxArm64 }
        set { linuxArm64 = newValue }
    }
}

