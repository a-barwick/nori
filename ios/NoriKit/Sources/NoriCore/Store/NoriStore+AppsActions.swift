import Foundation

extension NoriStore {
    public func repairGoogleConnection(connectionID: String, capabilities: [String]) async -> AuthAttempt? {
        let generation = sessionGeneration
        guard let connection = connections.first(where: { $0.id == connectionID }) else { return nil }
        do {
            let receipt = try await transport.performConnectionAction(
                connectionID: connectionID,
                expectedRevision: connection.revision,
                action: "repair",
                returnTo: NoriConfig.oauthCallbackURL,
                capabilities: capabilities,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return nil }
            if let index = connections.firstIndex(where: { $0.id == connectionID }) {
                connections[index] = receipt.resource
            }
            return receipt.auth
        } catch {
            guard generation == sessionGeneration else { return nil }
            _ = await handleOperationError(error)
            return nil
        }
    }
}
