import Foundation

// MARK: - Endpoint Router (Testable Pure Value Type)

public struct NoriEndpointRouter: Sendable, Hashable {
    public let baseURL: URL
    public let mode: ServerMode
    public let apiPrefix: String

    public init(baseURL: URL, mode: ServerMode, apiPrefix: String = "") {
        self.baseURL = baseURL
        self.mode = mode
        self.apiPrefix = apiPrefix
    }

    public init(profile: ServerProfile) throws {
        guard let url = profile.baseURL else {
            throw NoriAPIError(message: "Invalid server URL: \(profile.baseURLString)")
        }
        self.baseURL = url
        self.mode = profile.mode
        self.apiPrefix = profile.apiPrefix
    }

    /// Builds a URL for an unscoped endpoint (`/healthz`, `/api/users/resolve`).
    public func unscopedURL(path: String, queryItems: [URLQueryItem] = []) throws -> URL {
        let normalizedPath = path.hasPrefix("/") ? path : "/\(path)"
        let fullPath: String
        switch mode {
        case .directCore, .demo:
            fullPath = normalizedPath
        case .hostedBFF:
            fullPath = joinedHostedPath(normalizedPath)
        }
        return try buildURL(path: fullPath, queryItems: queryItems)
    }

    /// Builds a URL for a user-scoped endpoint (`/v1/...`, `/api/turn`, `/api/command`).
    public func scopedURL(path: String, userID: String?, queryItems: [URLQueryItem] = []) throws -> URL {
        let normalizedPath = path.hasPrefix("/") ? path : "/\(path)"
        let fullPath: String
        switch mode {
        case .directCore:
            guard let userID, !userID.isEmpty else {
                throw NoriAPIError(message: "User ID has not been resolved for direct core mode.")
            }
            let encodedUser = userID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? userID
            fullPath = "/users/\(encodedUser)\(normalizedPath)"
        case .hostedBFF:
            fullPath = joinedHostedPath(normalizedPath)
        case .demo:
            fullPath = normalizedPath
        }
        return try buildURL(path: fullPath, queryItems: queryItems)
    }

    private func joinedHostedPath(_ path: String) -> String {
        let cleanPrefix = apiPrefix.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        if cleanPrefix.isEmpty {
            return path
        }
        let prefixed = "/\(cleanPrefix)"
        // Avoid duplicating `/api/api/...` when prefix is `/api` and path starts with `/api/`.
        if path == prefixed || path.hasPrefix(prefixed + "/") {
            return path
        }
        return prefixed + path
    }

    private func buildURL(path: String, queryItems: [URLQueryItem]) throws -> URL {
        guard var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: true) else {
            throw NoriAPIError(message: "Could not construct URL from \(baseURL.absoluteString)")
        }
        let existing = components.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        if existing.isEmpty {
            components.path = path
        } else {
            components.path = "/\(existing)\(path)"
        }
        if !queryItems.isEmpty {
            components.queryItems = queryItems
        }
        guard let finalURL = components.url else {
            throw NoriAPIError(message: "Could not encode URL for path \(path)")
        }
        return finalURL
    }
}

// MARK: - Live HTTP + SSE Transport

public actor LiveNoriTransport: NoriTransport {
    public let profile: ServerProfile
    private let router: NoriEndpointRouter
    private let session: URLSession
    private let streamSession: URLSession
    private let keychain: KeychainStore
    private let encoder: JSONEncoder
    private let decoder: JSONDecoder
    private var resolvedUserID: String?

    public init(
        profile: ServerProfile,
        urlSession: URLSession? = nil,
        keychain: KeychainStore = KeychainStore()
    ) throws {
        self.profile = profile
        self.router = try NoriEndpointRouter(profile: profile)
        if let urlSession {
            self.session = urlSession
            self.streamSession = urlSession
        } else {
            let apiConfig = URLSessionConfiguration.default
            apiConfig.timeoutIntervalForRequest = 30
            apiConfig.timeoutIntervalForResource = 90
            apiConfig.requestCachePolicy = .reloadIgnoringLocalCacheData
            self.session = URLSession(configuration: apiConfig)

            let sseConfig = URLSessionConfiguration.default
            // Server rolls over SSE streams at 150s; give 185s timeout so rollover is clean.
            sseConfig.timeoutIntervalForRequest = 185
            sseConfig.timeoutIntervalForResource = 0
            sseConfig.requestCachePolicy = .reloadIgnoringLocalCacheData
            self.streamSession = URLSession(configuration: sseConfig)
        }
        self.keychain = keychain
        self.encoder = JSONEncoder()
        self.decoder = JSONDecoder()
    }

    // MARK: - Identity & Auth Headers

    private func ensureUserID() async throws -> String? {
        guard profile.mode == .directCore else { return nil }
        if let resolvedUserID { return resolvedUserID }
        let url = try router.unscopedURL(path: "/api/users/resolve")
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try encoder.encode(["email": profile.email])

        let (data, response) = try await session.data(for: req)
        try validateHTTPResponse(response, data: data)
        struct ResolveResponse: Decodable { let id: String }
        let resolved = try decoder.decode(ResolveResponse.self, from: data)
        self.resolvedUserID = resolved.id
        return resolved.id
    }

    private func applyHeaders(to request: inout URLRequest, contentType: String? = nil) {
        request.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        if let contentType {
            request.setValue(contentType, forHTTPHeaderField: "Content-Type")
        }
        if profile.mode == .hostedBFF, let token = keychain.loadToken(account: profile.id), !token.isEmpty {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
    }

    private func validateHTTPResponse(_ response: URLResponse, data: Data) throws {
        guard let http = response as? HTTPURLResponse else {
            throw NoriAPIError(message: "Invalid server response.")
        }
        guard (200..<300).contains(http.statusCode) else {
            if let payload = try? decoder.decode(APIErrorPayload.self, from: data) {
                throw NoriAPIError(
                    message: payload.error,
                    statusCode: http.statusCode,
                    code: payload.code,
                    retryable: payload.retryable ?? (http.statusCode >= 500 || http.statusCode == 429),
                    accepted: payload.accepted ?? false,
                    requestID: payload.requestID
                )
            }
            let fallback: String
            switch http.statusCode {
            case 401, 403: fallback = "Authentication required or user not recognized (\(http.statusCode))."
            case 404: fallback = "This resource is no longer available."
            case 409: fallback = "This item changed on the server. Refresh and try again."
            default: fallback = http.statusCode >= 500 ? "Nori could not be reached (\(http.statusCode))." : "Request failed (\(http.statusCode))."
            }
            throw NoriAPIError(
                message: fallback,
                statusCode: http.statusCode,
                retryable: http.statusCode >= 500 || http.statusCode == 429
            )
        }
    }

    private func getJSON<T: Decodable>(_ path: String, queryItems: [URLQueryItem] = []) async throws -> T {
        let userID = try await ensureUserID()
        let url = try router.scopedURL(path: path, userID: userID, queryItems: queryItems)
        var req = URLRequest(url: url)
        req.httpMethod = "GET"
        applyHeaders(to: &req)
        let (data, response) = try await session.data(for: req)
        try validateHTTPResponse(response, data: data)
        return try decoder.decode(T.self, from: data)
    }

    private func sendJSON<Body: Encodable, Response: Decodable>(
        _ path: String,
        method: String = "POST",
        body: Body
    ) async throws -> Response {
        let userID = try await ensureUserID()
        let url = try router.scopedURL(path: path, userID: userID)
        var req = URLRequest(url: url)
        req.httpMethod = method
        applyHeaders(to: &req, contentType: "application/json")
        req.httpBody = try encoder.encode(body)
        let (data, response) = try await session.data(for: req)
        try validateHTTPResponse(response, data: data)
        return try decoder.decode(Response.self, from: data)
    }

    /// Idempotent `/v1/*` mutation with automatic `/v1/mutations/{request_id}` recovery on ambiguous transport drop.
    private func mutate<Body: Encodable, Resource: Sendable & Codable & Hashable>(
        _ path: String,
        method: String = "POST",
        requestID: String,
        body: Body
    ) async throws -> MutationReceipt<Resource> {
        do {
            return try await sendJSON(path, method: method, body: body)
        } catch let apiErr as NoriAPIError {
            throw apiErr
        } catch {
            // Network drop before response arrived — check if the server recorded a receipt.
            if let encodedID = requestID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed),
               let receipt: MutationReceipt<Resource> = try? await getJSON("/v1/mutations/\(encodedID)") {
                return receipt
            }
            throw NoriAPIError(
                message: "Nori did not confirm this change. Retrying sends the same request ID so it cannot apply twice.",
                statusCode: 0,
                retryable: true,
                requestID: requestID
            )
        }
    }

    // MARK: - Ping & Capabilities

    public func ping() async -> HealthCheckResult {
        let start = ContinuousClock.now
        do {
            let url = try router.unscopedURL(path: "/healthz")
            var req = URLRequest(url: url)
            req.httpMethod = "GET"
            req.timeoutInterval = 8
            applyHeaders(to: &req)
            let (data, response) = try await session.data(for: req)
            let elapsed = start.duration(to: .now)
            let ms = Int(elapsed.components.seconds * 1000) + Int(elapsed.components.attoseconds / 1_000_000_000_000_000)
            try validateHTTPResponse(response, data: data)
            struct HealthBody: Decodable {
                let status: String
                let model: String?
            }
            let body = try decoder.decode(HealthBody.self, from: data)
            // Also validate directCore user resolution when pinging directCore so Setup catches bad emails early.
            if profile.mode == .directCore {
                _ = try await ensureUserID()
            }
            return HealthCheckResult(ok: body.status == "ok", status: body.status, model: body.model, latencyMs: max(1, ms))
        } catch {
            let elapsed = start.duration(to: .now)
            let ms = Int(elapsed.components.seconds * 1000) + Int(elapsed.components.attoseconds / 1_000_000_000_000_000)
            return HealthCheckResult(ok: false, status: "unreachable", model: nil, latencyMs: max(1, ms), errorMessage: error.localizedDescription)
        }
    }

    public func fetchCapabilities() async throws -> Capabilities {
        try await getJSON("/v1/capabilities")
    }

    public func fetchSession(before cursor: String?) async throws -> Session {
        var query: [URLQueryItem] = []
        if let cursor, !cursor.isEmpty {
            query.append(URLQueryItem(name: "before", value: cursor))
        }
        return try await getJSON("/v1/snapshot", queryItems: query)
    }

    public func fetchFullSnapshot() async throws -> NoriFullSnapshot {
        let caps = try? await fetchCapabilities()
        let session = try await fetchSession(before: nil)

        async let workTask = (try? await fetchWorkItems(bucket: nil)) ?? []
        async let interactionsTask = (try? await fetchInteractions()) ?? []
        async let mcpTask = (try? await fetchMCPServers()) ?? []
        async let connectionsTask = (try? await fetchConnections()) ?? []
        async let libraryTask = (try? await fetchLibrary()) ?? []
        async let memoriesTask = (try? await fetchCoreMemories()) ?? []
        async let logsTask = (try? await fetchMemoryLogs()) ?? []
        async let workingTask = (try? await fetchWorkingMemory()) ?? WorkingMemory()
        async let automationsTask = (try? await fetchAutomations()) ?? []
        async let goalsTask = (try? await fetchGoals()) ?? []
        async let permissionsTask = (try? await fetchPermissions()) ?? []
        async let preferencesTask = (try? await fetchPreferences()) ?? Preferences()

        let servers = await mcpTask
        var toolsByServer: [String: [MCPServerTool]] = [:]
        for server in servers {
            if let tools = try? await fetchMCPTools(serverID: server.id) {
                toolsByServer[server.id] = tools
            }
        }

        return await NoriFullSnapshot(
            capabilities: caps,
            session: session,
            workItems: workTask,
            interactions: interactionsTask,
            mcpServers: servers,
            mcpTools: toolsByServer,
            connections: connectionsTask,
            libraryItems: libraryTask,
            coreMemories: memoriesTask,
            memoryLogs: logsTask,
            workingMemory: workingTask,
            automations: automationsTask,
            goals: goalsTask,
            permissions: permissionsTask,
            preferences: preferencesTask
        )
    }

    // MARK: - Resumable SSE Event Stream (150s rollover + exponential backoff)

    public nonisolated func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                var cursor = max(0, position)
                var backoffSeconds: Double = 1.0

                while !Task.isCancelled {
                    do {
                        let userID = try await self.ensureUserID()
                        let url = try self.router.scopedURL(
                            path: "/v1/events",
                            userID: userID,
                            queryItems: [URLQueryItem(name: "after", value: String(cursor))]
                        )
                        var req = URLRequest(url: url)
                        req.httpMethod = "GET"
                        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
                        req.setValue("no-cache", forHTTPHeaderField: "Cache-Control")
                        if self.profile.mode == .hostedBFF,
                           let token = self.keychain.loadToken(account: self.profile.id),
                           !token.isEmpty {
                            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
                        }

                        let (bytes, response) = try await self.streamSession.bytes(for: req)
                        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
                            let code = (response as? HTTPURLResponse)?.statusCode ?? 0
                            throw NoriAPIError(message: "Event stream rejected (\(code)).", statusCode: code, retryable: code >= 500 || code == 429)
                        }

                        // Connected cleanly — reset backoff.
                        backoffSeconds = 1.0
                        var parser = SSEParser()
                        let jsonDecoder = JSONDecoder()

                        // Note: URLSession.AsyncBytes.lines omits blank lines on Darwin,
                        // which breaks SSE event dispatch (`\n\n`). Read raw UTF-8 lines directly.
                        var lineBuffer: [UInt8] = []
                        lineBuffer.reserveCapacity(1024)
                        for try await byte in bytes {
                            if Task.isCancelled { break }
                            if byte == 0x0A { // '\n'
                                let line = String(decoding: lineBuffer, as: UTF8.self)
                                lineBuffer.removeAll(keepingCapacity: true)
                                if let frame = parser.feedLine(line),
                                   let event = NoriStreamEvent.decode(from: frame, decoder: jsonDecoder) {
                                    if let seq = event.sequence, seq > cursor {
                                        cursor = seq
                                    }
                                    continuation.yield(event)
                                }
                            } else {
                                lineBuffer.append(byte)
                            }
                        }

                        if !lineBuffer.isEmpty {
                            let line = String(decoding: lineBuffer, as: UTF8.self)
                            if let frame = parser.feedLine(line),
                               let event = NoriStreamEvent.decode(from: frame, decoder: jsonDecoder) {
                                if let seq = event.sequence, seq > cursor {
                                    cursor = seq
                                }
                                continuation.yield(event)
                            }
                        }

                        if let trailing = parser.flush(),
                           let event = NoriStreamEvent.decode(from: trailing, decoder: jsonDecoder) {
                            if let seq = event.sequence, seq > cursor {
                                cursor = seq
                            }
                            continuation.yield(event)
                        }

                        // Clean EOF (e.g. 150s server rollover): reconnect immediately without backoff penalty.
                        if !Task.isCancelled {
                            try? await Task.sleep(for: .milliseconds(250))
                        }
                    } catch is CancellationError {
                        break
                    } catch {
                        let delay = min(backoffSeconds, 15.0)
                        backoffSeconds = min(backoffSeconds * 2.0, 15.0)
                        try? await Task.sleep(for: .seconds(delay))
                    }
                }
                continuation.finish()
            }

            continuation.onTermination = { _ in
                task.cancel()
            }
        }
    }

    // MARK: - Conversation & Uploads

    public func sendTurn(id: String, message: String, attachmentIDs: [String]) async throws -> Session {
        struct TurnBody: Encodable {
            let id: String
            let message: String
            let attachments: [String]?
        }
        return try await sendJSON(
            "/api/turn",
            method: "POST",
            body: TurnBody(id: id, message: message, attachments: attachmentIDs.isEmpty ? nil : attachmentIDs)
        )
    }

    public func sendTaskCommand(_ command: TaskCommand) async throws -> TaskCommandResponse {
        try await sendJSON("/api/command", method: "POST", body: command)
    }

    public func uploadFile(id: String, filename: String, mediaType: String, data: Data) async throws -> Attachment {
        let userID = try await ensureUserID()
        let url = try router.scopedURL(
            path: "/v1/uploads",
            userID: userID,
            queryItems: [
                URLQueryItem(name: "id", value: id),
                URLQueryItem(name: "filename", value: filename)
            ]
        )
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        applyHeaders(to: &req, contentType: mediaType)
        req.httpBody = data
        let (responseData, response) = try await session.data(for: req)
        try validateHTTPResponse(response, data: responseData)
        return try decoder.decode(Attachment.self, from: responseData)
    }

    // MARK: - Work & Interactions

    public func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem] {
        var query: [URLQueryItem] = []
        if let bucket {
            query.append(URLQueryItem(name: "bucket", value: bucket.rawValue))
        }
        let resp: ListResponse<WorkItem> = try await getJSON("/v1/work", queryItems: query)
        return resp.items
    }

    public func fetchWorkDetail(taskID: String) async throws -> WorkDetail {
        try await getJSON("/v1/work/\(taskID)")
    }

    public func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem> {
        struct StopReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: [String: String]
        }
        return try await mutate(
            "/v1/work/\(taskID)/actions",
            requestID: requestID,
            body: StopReq(request_id: requestID, expected_revision: 0, value: ["action": "stop"])
        )
    }

    public func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem> {
        struct ContinueValue: Encodable {
            let action: String
            let budget_usd: Double
        }
        struct ContinueReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: ContinueValue
        }
        return try await mutate(
            "/v1/work/\(taskID)/actions",
            requestID: requestID,
            body: ContinueReq(request_id: requestID, expected_revision: 0, value: ContinueValue(action: "continue", budget_usd: budgetUSD))
        )
    }

    public func fetchInteractions() async throws -> [Interaction] {
        let resp: ListResponse<Interaction> = try await getJSON("/v1/interactions")
        return resp.items
    }

    public func respondToInteraction(
        id: String,
        expectedRevision: Int,
        answer: InteractionAnswer,
        requestID: String
    ) async throws -> MutationReceipt<Interaction> {
        struct RespReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: [String: AnyJSON]
        }
        return try await mutate(
            "/v1/interactions/\(id)/responses",
            requestID: requestID,
            body: RespReq(request_id: requestID, expected_revision: expectedRevision, value: answer.payloadDictionary)
        )
    }

    // MARK: - Apps: MCP Servers & Connections

    public func fetchMCPServers() async throws -> [MCPServer] {
        let resp: ListResponse<MCPServer> = try await getJSON("/v1/mcp")
        return resp.items
    }

    public func fetchMCPTools(serverID: String) async throws -> [MCPServerTool] {
        let resp: ListResponse<MCPServerTool> = try await getJSON("/v1/mcp/\(serverID)/tools")
        return resp.items
    }

    public func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer> {
        struct CreateReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: MCPServerInput
        }
        return try await mutate(
            "/v1/mcp",
            requestID: requestID,
            body: CreateReq(request_id: requestID, expected_revision: 0, value: input)
        )
    }

    public func performMCPAction(
        serverID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        struct ActionReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: [String: String]
        }
        return try await mutate(
            "/v1/mcp/\(serverID)/actions",
            requestID: requestID,
            body: ActionReq(request_id: requestID, expected_revision: expectedRevision, value: ["action": action])
        )
    }

    public func setMCPToolExposure(
        serverID: String,
        expectedRevision: Int,
        toolName: String,
        exposure: MCPToolExposure,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        struct ExposureVal: Encodable {
            let action: String
            let tool: String
            let exposure: String
        }
        struct ExposureReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: ExposureVal
        }
        return try await mutate(
            "/v1/mcp/\(serverID)/actions",
            requestID: requestID,
            body: ExposureReq(
                request_id: requestID,
                expected_revision: expectedRevision,
                value: ExposureVal(action: "set_exposure", tool: toolName, exposure: exposure.rawValue)
            )
        )
    }

    public func fetchConnections() async throws -> [Connection] {
        let resp: ListResponse<Connection> = try await getJSON("/v1/connections")
        return resp.items
    }

    public func performConnectionAction(
        connectionID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<Connection> {
        struct ActionReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: [String: String]
        }
        return try await mutate(
            "/v1/connections/\(connectionID)/actions",
            requestID: requestID,
            body: ActionReq(request_id: requestID, expected_revision: expectedRevision, value: ["action": action])
        )
    }

    // MARK: - Library & Search

    public func fetchLibrary() async throws -> [LibraryItem] {
        let resp: ListResponse<LibraryItem> = try await getJSON("/v1/library")
        return resp.items
    }

    public func fetchLibraryItem(id: String) async throws -> LibraryItem {
        try await getJSON("/v1/library/\(id)")
    }

    public func search(query: String) async throws -> SearchResponse {
        try await getJSON("/v1/search", queryItems: [URLQueryItem(name: "q", value: query)])
    }

    public func reviseLibraryItem(
        id: String,
        expectedRevision: Int,
        instruction: String,
        requestID: String
    ) async throws -> MutationReceipt<LibraryItem> {
        struct ReviseReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: [String: String]
        }
        return try await mutate(
            "/v1/library/\(id)/actions",
            requestID: requestID,
            body: ReviseReq(
                request_id: requestID,
                expected_revision: expectedRevision,
                value: ["action": "revise", "instruction": instruction]
            )
        )
    }

    // MARK: - Memory

    public func fetchCoreMemories() async throws -> [MemoryFact] {
        let resp: ListResponse<MemoryFact> = try await getJSON("/v1/memory")
        return resp.items
    }

    public func fetchMemoryLogs() async throws -> [MemoryLog] {
        let resp: ListResponse<MemoryLog> = try await getJSON("/v1/memory/logs")
        return resp.items
    }

    public func fetchWorkingMemory() async throws -> WorkingMemory {
        try await getJSON("/v1/memory/working")
    }

    public func createMemory(
        text: String,
        section: String?,
        about: String?,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryFact> {
        struct MemVal: Encodable {
            let text: String
            let section: String?
            let about: String?
            let pinned: Bool
        }
        struct MemReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: MemVal
        }
        return try await mutate(
            "/v1/memory",
            requestID: requestID,
            body: MemReq(request_id: requestID, expected_revision: 0, value: MemVal(text: text, section: section, about: about, pinned: pinned))
        )
    }

    public func performMemoryAction(
        id: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<MemoryFact> {
        struct ActionReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: [String: String]
        }
        return try await mutate(
            "/v1/memory/\(id)/actions",
            requestID: requestID,
            body: ActionReq(request_id: requestID, expected_revision: expectedRevision, value: ["action": action])
        )
    }

    public func performMemoryLogEntryAction(
        date: String,
        entryID: String,
        expectedRevision: Int,
        action: String,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryLog> {
        struct ActionVal: Encodable {
            let action: String
            let pinned: Bool?
        }
        struct ActionReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: ActionVal
        }
        return try await mutate(
            "/v1/memory/logs/\(date)/\(entryID)/actions",
            requestID: requestID,
            body: ActionReq(
                request_id: requestID,
                expected_revision: expectedRevision,
                value: ActionVal(action: action, pinned: action == "promote" ? pinned : nil)
            )
        )
    }

    public func performWorkingMemoryAction(
        itemID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<WorkingMemory> {
        struct ActionReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: [String: String]
        }
        return try await mutate(
            "/v1/memory/working/\(itemID)/actions",
            requestID: requestID,
            body: ActionReq(request_id: requestID, expected_revision: expectedRevision, value: ["action": action])
        )
    }

    // MARK: - Automations, Goals, Permissions, Preferences

    public func fetchAutomations() async throws -> [Automation] {
        let resp: ListResponse<Automation> = try await getJSON("/v1/automations")
        return resp.items
    }

    public func performAutomationAction(
        id: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<Automation> {
        struct ActionReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: [String: String]
        }
        return try await mutate(
            "/v1/automations/\(id)/actions",
            requestID: requestID,
            body: ActionReq(request_id: requestID, expected_revision: expectedRevision, value: ["action": action])
        )
    }

    public func fetchGoals() async throws -> [Goal] {
        let resp: ListResponse<Goal> = try await getJSON("/v1/goals")
        return resp.items
    }

    public func fetchPermissions() async throws -> [PermissionRule] {
        let resp: ListResponse<PermissionRule> = try await getJSON("/v1/permissions")
        return resp.items
    }

    public func fetchPreferences() async throws -> Preferences {
        try await getJSON("/v1/preferences")
    }

    public func savePreferences(_ preferences: Preferences, requestID: String) async throws -> MutationReceipt<Preferences> {
        struct PrefVal: Encodable {
            let timezone: String
            let locale: String?
            let working_hours: [WorkingHoursSlot]
            let quiet_hours: QuietHours?
            let default_destination_ids: [String]
            let account_defaults: [String: String]
            let budgets: BudgetPreferences
            let input: InputPreferences?
        }
        struct PrefReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: PrefVal
        }
        let val = PrefVal(
            timezone: preferences.timezone,
            locale: preferences.locale,
            working_hours: preferences.workingHours,
            quiet_hours: preferences.quietHours,
            default_destination_ids: preferences.defaultDestinationIDs,
            account_defaults: preferences.accountDefaults,
            budgets: preferences.budgets,
            input: preferences.input
        )
        return try await mutate(
            "/v1/preferences",
            method: "PUT",
            requestID: requestID,
            body: PrefReq(request_id: requestID, expected_revision: preferences.revision, value: val)
        )
    }
}
