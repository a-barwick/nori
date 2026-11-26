import Foundation
import Security

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
