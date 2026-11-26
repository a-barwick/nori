import Foundation
import Security
import Darwin

func verifiedCaller(for account: String) -> Bool {
    let parentPID = getppid()
    let attributes = [kSecGuestAttributePid as String: parentPID] as CFDictionary
    var parent: SecCode?
    var ownCode: SecCode?
    guard SecCodeCopyGuestWithAttributes(nil, attributes, [], &parent) == errSecSuccess,
          let parent,
          SecCodeCheckValidity(parent, [], nil) == errSecSuccess,
          SecCodeCopySelf([], &ownCode) == errSecSuccess,
          let ownCode,
          SecCodeCheckValidity(ownCode, [], nil) == errSecSuccess else {
        return false
    }
    var parentStatic: SecStaticCode?
    var ownStatic: SecStaticCode?
    guard SecCodeCopyStaticCode(parent, [], &parentStatic) == errSecSuccess,
          SecCodeCopyStaticCode(ownCode, [], &ownStatic) == errSecSuccess,
          let parentStatic, let ownStatic else {
        return false
    }

    func executablePath(_ pid: pid_t) -> URL? {
        var path = [CChar](repeating: 0, count: 4096)
        guard proc_pidpath(pid, &path, UInt32(path.count)) > 0 else { return nil }
        return URL(fileURLWithPath: String(cString: path)).resolvingSymlinksInPath()
    }
    guard let ownURL = executablePath(getpid()),
          let parentURL = executablePath(parentPID) else { return false }

    func signingInformation(_ code: SecStaticCode) -> [String: Any]? {
        var details: CFDictionary?
        guard SecCodeCopySigningInformation(code, SecCSFlags(rawValue: kSecCSSigningInformation), &details) == errSecSuccess,
              let info = details as? [String: Any] else { return nil }
        return info
    }

    guard let ownInfo = signingInformation(ownStatic),
          let parentInfo = signingInformation(parentStatic),
          ownInfo[kSecCodeInfoIdentifier as String] as? String == "com.nori.mac.keychain" else { return false }
    let ownTeam = ownInfo[kSecCodeInfoTeamIdentifier as String] as? String
    let parentTeam = parentInfo[kSecCodeInfoTeamIdentifier as String] as? String
    #if NORI_ADHOC
    guard ownTeam == parentTeam else { return false }
    #else
    guard let ownTeam, !ownTeam.isEmpty, ownTeam == parentTeam else { return false }
    #if NORI_RELEASE
    let requirementText = "anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists"
    #else
    let requirementText = "anchor apple generic"
    #endif
    var requirement: SecRequirement?
    guard SecRequirementCreateWithString(requirementText as CFString, [], &requirement) == errSecSuccess,
          let requirement,
          SecCodeCheckValidity(parent, [], requirement) == errSecSuccess,
          SecCodeCheckValidity(ownCode, [], requirement) == errSecSuccess else { return false }
    #endif

    let executableDirectory = ownURL.deletingLastPathComponent()
    let expectedParent = parentURL
    let connector = executableDirectory.appendingPathComponent("nori-connector")
    let app = executableDirectory.appendingPathComponent("Nori")
    let stateDir = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/Application Support/Nori/Home AI").path
    if expectedParent == connector {
        return parentInfo[kSecCodeInfoIdentifier as String] as? String == "com.nori.mac.connector" &&
            [stateDir, stateDir + ".pairing", stateDir + ".pending-revocation"].contains(account)
    }
    if expectedParent == app {
        return parentInfo[kSecCodeInfoIdentifier as String] as? String == "com.nori.mac" &&
            account.hasPrefix("com.nori.mac.google-session.v1:") && account.count <= 512
    }
    return false
}

struct Request: Decodable {
    let action: String
    let account: String
    let value: String?
}

struct Response: Encodable {
    var value: String?
    var missing: Bool?
    var error: String?
}

func respond(_ response: Response) {
    if let data = try? JSONEncoder().encode(response) {
        FileHandle.standardOutput.write(data)
    }
}

do {
    let input = FileHandle.standardInput.readDataToEndOfFile()
    let request = try JSONDecoder().decode(Request.self, from: input)
    guard verifiedCaller(for: request.account) else {
        respond(Response(error: "The calling process cannot access Nori credentials."))
        exit(1)
    }
    let query: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: "com.nori.connector.credentials",
        kSecAttrAccount as String: request.account
    ]

    switch request.action {
    case "read":
        var search = query
        search[kSecReturnData as String] = true
        search[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        let status = SecItemCopyMatching(search as CFDictionary, &item)
        if status == errSecItemNotFound {
            respond(Response(missing: true))
        } else if status == errSecSuccess, let data = item as? Data,
                  let value = String(data: data, encoding: .utf8) {
            respond(Response(value: value))
        } else {
            respond(Response(error: "Unable to read connector credentials (\(status))."))
        }
    case "write":
        guard let value = request.value, let data = value.data(using: .utf8) else {
            respond(Response(error: "Connector credentials are missing."))
            break
        }
        let update = [kSecValueData as String: data] as CFDictionary
        var status = SecItemUpdate(query as CFDictionary, update)
        if status == errSecItemNotFound {
            var item = query
            item[kSecValueData as String] = data
            item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            status = SecItemAdd(item as CFDictionary, nil)
        }
        if status == errSecSuccess {
            respond(Response())
        } else {
            respond(Response(error: "Unable to save connector credentials (\(status))."))
        }
    case "delete":
        let status = SecItemDelete(query as CFDictionary)
        if status == errSecItemNotFound {
            respond(Response(missing: true))
        } else if status == errSecSuccess {
            respond(Response())
        } else {
            respond(Response(error: "Unable to remove connector credentials (\(status))."))
        }
    default:
        respond(Response(error: "Unknown credential operation."))
    }
} catch {
    respond(Response(error: "Unable to read connector credential request."))
}
