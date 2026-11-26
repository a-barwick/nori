import Foundation

extension NoriStore {
    public func refreshApps() async {
        let generation = sessionGeneration
        let activeTransport = transport
        async let serversResult: Result<[MCPServer], Error> = {
            do { return .success(try await activeTransport.fetchMCPServers()) }
            catch { return .failure(error) }
        }()
        async let connectionsResult: Result<[Connection], Error> = {
            do { return .success(try await activeTransport.fetchConnections()) }
            catch { return .failure(error) }
        }()
        async let integrationsResult: Result<[IntegrationSummary], Error> = {
            do { return .success(try await activeTransport.fetchIntegrations()) }
            catch { return .failure(error) }
        }()

        let (serversRes, connsRes, integrationsRes) = await (serversResult, connectionsResult, integrationsResult)
        guard generation == sessionGeneration else { return }
        if case .failure(let err) = serversRes, await handleAuthFailureIfNeeded(err) { return }
        if case .failure(let err) = connsRes, await handleAuthFailureIfNeeded(err) { return }
        if case .failure(let err) = integrationsRes, await handleAuthFailureIfNeeded(err) { return }

        if case .success(let servers) = serversRes {
            self.mcpServers = servers
            let toolsByServer = await withTaskGroup(
                of: (String, [MCPServerTool]?).self,
                returning: [String: [MCPServerTool]].self
            ) { group in
                for server in servers {
                    let serverID = server.id
                    group.addTask {
                        let tools = try? await activeTransport.fetchMCPTools(serverID: serverID)
                        return (serverID, tools)
                    }
                }
                var collected: [String: [MCPServerTool]] = [:]
                for await (serverID, tools) in group {
                    if let tools {
                        collected[serverID] = tools
                    }
                }
                return collected
            }
            guard generation == sessionGeneration else { return }
            self.mcpTools = toolsByServer
        }
        if case .success(let conns) = connsRes {
            self.connections = conns
        }
        if case .success(let items) = integrationsRes {
            self.integrations = items
        }
        if assistantSummary != nil {
            assistantSummary?.appsAttention = computedAppsAttentionCount
        }
    }

    public func addMCPServer(
        name: String,
        endpoint: String,
        auth: MCPAuthKind = .oauth,
        defaultExposure: MCPToolExposure = .discoverable
    ) async {
        let generation = sessionGeneration
        let cleanName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanEndpoint = endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanName.isEmpty, !cleanEndpoint.isEmpty else { return }
        do {
            let input = MCPServerInput(
                name: cleanName,
                transport: .http,
                endpoint: cleanEndpoint,
                auth: auth,
                enabled: true,
                exposure: MCPExposureConfig(default: defaultExposure)
            )
            let receipt = try await transport.createMCPServer(input: input, requestID: UUID().uuidString.lowercased())
            guard generation == sessionGeneration else { return }
            mcpServers.append(receipt.resource)
            let tools = try? await transport.fetchMCPTools(serverID: receipt.resource.id)
            guard generation == sessionGeneration else { return }
            if let tools {
                mcpTools[receipt.resource.id] = tools
            }
            showToast("Added \(cleanName)")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func setToolExposure(serverID: String, toolName: String, exposure: MCPToolExposure) async {
        let generation = sessionGeneration
        guard let server = mcpServers.first(where: { $0.id == serverID }) else { return }
        do {
            let receipt = try await transport.setMCPToolExposure(
                serverID: serverID,
                expectedRevision: server.revision,
                toolName: toolName,
                exposure: exposure,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            let tools = try? await transport.fetchMCPTools(serverID: serverID)
            guard generation == sessionGeneration else { return }
            if let tools {
                mcpTools[serverID] = tools
            }
            showToast("Tool set to \(exposure.displayLabel)")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func reconnectMCPServer(serverID: String) async {
        let generation = sessionGeneration
        guard let server = mcpServers.first(where: { $0.id == serverID }) else { return }
        do {
            let receipt = try await transport.performMCPAction(
                serverID: serverID,
                expectedRevision: server.revision,
                action: "reconnect",
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            let tools = try? await transport.fetchMCPTools(serverID: serverID)
            guard generation == sessionGeneration else { return }
            if let tools {
                mcpTools[serverID] = tools
            }
            showToast("Reconnected \(server.name)")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    @discardableResult
    public func startMCPOAuth(serverID: String) async -> AuthAttempt? {
        let generation = sessionGeneration
        guard let server = mcpServers.first(where: { $0.id == serverID }) else { return nil }
        do {
            let receipt = try await transport.performMCPAction(
                serverID: serverID,
                expectedRevision: server.revision,
                action: "sign_in",
                returnTo: NoriConfig.oauthCallbackURL,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return nil }
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            let tools = try? await transport.fetchMCPTools(serverID: serverID)
            guard generation == sessionGeneration else { return nil }
            if let tools {
                mcpTools[serverID] = tools
            }
            if receipt.auth == nil {
                showToast("Signed in to \(server.name)")
            }
            return receipt.auth
        } catch {
            guard generation == sessionGeneration else { return nil }
            _ = await handleOperationError(error)
            return nil
        }
    }

    @discardableResult
    public func completeMCPOAuth(serverID: String, callbackURL: URL? = nil) async -> AuthAttempt? {
        let generation = sessionGeneration
        if callbackURL != nil {
            async let appsTask: Void = refreshApps()
            async let workTask: Void = refreshWorkAndInteractions()
            async let summaryTask: Void = refreshSummary()
            _ = await (appsTask, workTask, summaryTask)
            guard generation == sessionGeneration else { return nil }
            if let server = mcpServers.first(where: { $0.id == serverID }),
               server.observed.status == .connected {
                showToast("Signed in to \(server.name)")
            }
            return nil
        }
        return await startMCPOAuth(serverID: serverID)
    }

    @discardableResult
    public func saveMCPCredential(serverID: String, secret: String) async -> Bool {
        let generation = sessionGeneration
        let trimmed = secret.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty,
              let server = mcpServers.first(where: { $0.id == serverID }) else {
            return false
        }
        do {
            let receipt = try await transport.setMCPCredential(
                serverID: serverID,
                expectedRevision: server.revision,
                secret: trimmed,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return false }
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            let tools = try? await transport.fetchMCPTools(serverID: serverID)
            guard generation == sessionGeneration else { return false }
            if let tools {
                mcpTools[serverID] = tools
            }
            showToast("Saved credential for \(server.name)")
            return true
        } catch {
            guard generation == sessionGeneration else { return false }
            _ = await handleOperationError(error)
            return false
        }
    }

    @discardableResult
    public func connectConnection(connectionID: String) async -> AuthAttempt? {
        let generation = sessionGeneration
        guard let conn = connections.first(where: { $0.id == connectionID }) else { return nil }
        do {
            let receipt = try await transport.performConnectionAction(
                connectionID: connectionID,
                expectedRevision: conn.revision,
                action: "connect",
                returnTo: NoriConfig.oauthCallbackURL,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return nil }
            if let idx = connections.firstIndex(where: { $0.id == connectionID }) {
                connections[idx] = receipt.resource
            }
            if receipt.auth == nil {
                showToast("Connected \(conn.title)")
            }
            return receipt.auth
        } catch {
            guard generation == sessionGeneration else { return nil }
            _ = await handleOperationError(error)
            return nil
        }
    }

    public func connectGoogle(capabilities: [String]) async -> AuthAttempt? {
        let generation = sessionGeneration
        do {
            let connection: Connection
            if let existing = connections.first(where: {
                $0.integrationID == "google" && $0.status != .disconnecting
            }) {
                connection = existing
            } else {
                let receipt = try await transport.createConnection(
                    integrationID: "google", requestID: UUID().uuidString.lowercased()
                )
                guard generation == sessionGeneration else { return nil }
                connection = receipt.resource
                connections.append(connection)
            }
            let receipt = try await transport.performConnectionAction(
                connectionID: connection.id,
                expectedRevision: connection.revision,
                action: "connect",
                returnTo: NoriConfig.oauthCallbackURL,
                capabilities: capabilities,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return nil }
            if let index = connections.firstIndex(where: { $0.id == connection.id }) {
                connections[index] = receipt.resource
            }
            return receipt.auth
        } catch {
            guard generation == sessionGeneration else { return nil }
            _ = await handleOperationError(error)
            return nil
        }
    }

    public func startGoogleDrivePicker(connectionID: String) async -> AuthAttempt? {
        let generation = sessionGeneration
        do {
            let attempt = try await transport.startGoogleDrivePicker(
                connectionID: connectionID, returnTo: NoriConfig.oauthCallbackURL
            )
            guard generation == sessionGeneration else { return nil }
            return attempt
        } catch {
            guard generation == sessionGeneration else { return nil }
            _ = await handleOperationError(error)
            return nil
        }
    }

    public func loadGoogleDriveFiles(connectionID: String) async -> [ConnectionResource]? {
        let generation = sessionGeneration
        do {
            let resources = try await transport.fetchConnectionResources(connectionID: connectionID)
            guard generation == sessionGeneration else { return nil }
            return resources.filter { $0.kind == "drive_file" }
        } catch {
            guard generation == sessionGeneration else { return nil }
            _ = await handleOperationError(error)
            return nil
        }
    }

    public func disconnectGoogle(connectionID: String) async {
        let generation = sessionGeneration
        guard let connection = connections.first(where: { $0.id == connectionID }) else { return }
        do {
            let receipt = try await transport.performConnectionAction(
                connectionID: connectionID,
                expectedRevision: connection.revision,
                action: "disconnect",
                returnTo: nil,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            if let index = connections.firstIndex(where: { $0.id == connectionID }) {
                connections[index] = receipt.resource
            }
            await refreshApps()
            guard generation == sessionGeneration else { return }
            if let message = connections.first(where: { $0.id == connectionID })?.health.message, !message.isEmpty {
                showToast(message)
            } else {
                showToast("Disconnected Google")
            }
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    // MARK: - Library

    public func refreshLibrary() async {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchLibrary()
            guard generation == sessionGeneration else { return }
            libraryItems = latest
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error, showToast: false)
        }
    }

    public func loadLibraryItem(id: String) async -> LibraryItem? {
        let generation = sessionGeneration
        do {
            let item = try await transport.fetchLibraryItem(id: id)
            guard generation == sessionGeneration else { return nil }
            if let idx = libraryItems.firstIndex(where: { $0.id == item.id }) {
                libraryItems[idx] = item
                return item
            } else {
                libraryItems.insert(item, at: 0)
                return item
            }
        } catch {
            guard generation == sessionGeneration else { return nil }
            _ = await handleOperationError(error, showToast: false)
            return libraryItems.first(where: { $0.id == id })
        }
    }

    public func loadLibraryItemFile(id: String, format: String? = nil) async throws -> LibraryFilePayload {
        let generation = sessionGeneration
        do {
            let payload = try await transport.fetchLibraryItemFile(id: id, format: format)
            guard generation == sessionGeneration else { throw CancellationError() }
            if payload.mediaType.lowercased().hasPrefix("text/"),
               let text = payload.utf8Text {
                libraryBodyCache[id] = text
            }
            return payload
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func searchLibrary(query: String) async {
        let generation = sessionGeneration
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            searchHits = []
            return
        }
        do {
            let response = try await transport.search(query: trimmed)
            guard generation == sessionGeneration else { return }
            searchHits = response.items
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error, showToast: false)
        }
    }

    @discardableResult
    public func reviseDocument(itemID: String, instruction: String) async -> LibraryRevisionResult? {
        let generation = sessionGeneration
        let trimmed = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        let item: LibraryItem
        if let cached = libraryItems.first(where: { $0.id == itemID }) {
            item = cached
        } else if let loaded = await loadLibraryItem(id: itemID) {
            item = loaded
        } else {
            return nil
        }
        do {
            let receipt = try await transport.reviseLibraryItem(
                id: itemID,
                expectedRevision: item.revision,
                instruction: trimmed,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return nil }
            if let idx = libraryItems.firstIndex(where: { $0.id == itemID }) {
                libraryItems[idx] = receipt.resource
            } else {
                libraryItems.insert(receipt.resource, at: 0)
            }
            if receipt.status == .queued {
                showToast("Revision started")
                await refreshWorkAndInteractions()
                guard generation == sessionGeneration else { return nil }
            } else {
                showToast("Revision saved (v\(receipt.resource.version))")
            }
            return LibraryRevisionResult(
                status: receipt.status,
                taskID: receipt.work?.taskID,
                item: receipt.resource
            )
        } catch {
            guard generation == sessionGeneration else { return nil }
            _ = await handleOperationError(error)
            return nil
        }
    }
}
