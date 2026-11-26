import Foundation
import ServiceManagement

let service = SMAppService.agent(plistName: "com.nori.mac.home-ai.plist")

func backgroundStatus() -> String {
    switch service.status {
    case .enabled:
        return "enabled"
    case .requiresApproval:
        return "approval_required"
    case .notRegistered:
        return "off"
    case .notFound:
        let plist = Bundle.main.bundleURL.appendingPathComponent("Contents/Library/LaunchAgents/com.nori.mac.home-ai.plist")
        return FileManager.default.fileExists(atPath: plist.path) ? "off" : "unavailable"
    default:
        return "unavailable"
    }
}

func writeResult() throws {
    let data = try JSONSerialization.data(withJSONObject: ["background": backgroundStatus()])
    FileHandle.standardOutput.write(data)
    FileHandle.standardOutput.write(Data("\n".utf8))
}

do {
    guard CommandLine.arguments.count == 2 else {
        throw NSError(domain: "NoriService", code: 1, userInfo: [NSLocalizedDescriptionKey: "Choose a service action."])
    }
    switch CommandLine.arguments[1] {
    case "status":
        break
    case "register":
        if service.status == .notRegistered || service.status == .notFound {
            try service.register()
        }
    case "unregister":
        if service.status == .enabled || service.status == .requiresApproval {
            try service.unregister()
        }
    case "settings":
        SMAppService.openSystemSettingsLoginItems()
    default:
        throw NSError(domain: "NoriService", code: 2, userInfo: [NSLocalizedDescriptionKey: "Unknown service action."])
    }
    try writeResult()
} catch {
    let message = "Unable to change background access: \(error.localizedDescription)\n"
    FileHandle.standardError.write(Data(message.utf8))
    exit(1)
}
