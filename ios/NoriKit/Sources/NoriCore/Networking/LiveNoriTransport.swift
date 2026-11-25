import Foundation

// MARK: - Endpoint Router (Testable Pure Value Type)

public struct NoriEndpointRouter: Sendable, Hashable {
    public let baseURL: URL

    public init(baseURL: URL = NoriConfig.serverURL) {
        self.baseURL = baseURL
    }

    /// Builds a URL for an unscoped endpoint (`/readyz`, `/api/users/resolve`).
    public func unscopedURL(path: String, queryItems: [URLQueryItem] = []) throws -> URL {
        let normalizedPath = path.hasPrefix("/") ? path : "/\(path)"
        return try buildURL(path: normalizedPath, queryItems: queryItems)
    }

    /// Builds a URL for a user-scoped endpoint (`/v1/...`, `/api/turn`, `/api/command`).
    public func scopedURL(path: String, userID: String, queryItems: [URLQueryItem] = []) throws -> URL {
        guard !userID.isEmpty else {
            throw NoriAPIError(message: "User ID has not been resolved.", statusCode: 401)
        }
        let normalizedPath = path.hasPrefix("/") ? path : "/\(path)"
        let encodedUser = userID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? userID
        return try buildURL(path: "/users/\(encodedUser)\(normalizedPath)", queryItems: queryItems)
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
    public let baseURL: URL
    private let router: NoriEndpointRouter
    private let session: URLSession
    private let streamSession: URLSession
    private let authProvider: AuthTokenProvider
    private let encoder: JSONEncoder
    private let decoder: JSONDecoder
    private let sseInitialBackoffSeconds: Double
    private let sseRolloverDelayMilliseconds: Int
    private var resolvedUserID: String?

    public init(
        baseURL: URL = NoriConfig.serverURL,
        authProvider: AuthTokenProvider = AuthTokenProvider(),
        urlSession: URLSession? = nil,
        sseInitialBackoffSeconds: Double = 1.0,
        sseRolloverDelayMilliseconds: Int = 250
    ) {
        self.baseURL = baseURL
        self.router = NoriEndpointRouter(baseURL: baseURL)
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
        self.authProvider = authProvider
        self.encoder = JSONEncoder()
        self.decoder = JSONDecoder()
        self.sseInitialBackoffSeconds = max(0.01, sseInitialBackoffSeconds)
        self.sseRolloverDelayMilliseconds = max(0, sseRolloverDelayMilliseconds)
    }

    public func clearCachedIdentity() {
        resolvedUserID = nil
    }

    // MARK: - Identity & Auth Headers

    private func ensureUserID() async throws -> String {
        if let resolvedUserID { return resolvedUserID }
        let url = try router.unscopedURL(path: "/api/users/resolve")
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.httpBody = try encoder.encode([String: String]())
        let (data, response) = try await performAuthenticated(req, contentType: "application/json")
        try validateHTTPResponse(response, data: data)
        struct ResolveResponse: Decodable { let id: String }
        let resolved = try decoder.decode(ResolveResponse.self, from: data)
        self.resolvedUserID = resolved.id
        return resolved.id
    }

    private func performAuthenticated(_ baseRequest: URLRequest, contentType: String? = nil) async throws -> (Data, URLResponse) {
        let token = try await authProvider.validIDToken(forceRefresh: false)
        var req = baseRequest
        req.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        if let contentType {
            req.setValue(contentType, forHTTPHeaderField: "Content-Type")
        }
        let (data, response) = try await session.data(for: req)
        if let http = response as? HTTPURLResponse, http.statusCode == 401 {
            // Retry once with a freshly refreshed Google ID token.
            let freshToken = try await authProvider.validIDToken(forceRefresh: true)
            var retryReq = baseRequest
            retryReq.setValue("no-store", forHTTPHeaderField: "Cache-Control")
            retryReq.setValue("Bearer \(freshToken)", forHTTPHeaderField: "Authorization")
            if let contentType {
                retryReq.setValue(contentType, forHTTPHeaderField: "Content-Type")
            }
            return try await session.data(for: retryReq)
        }
        return (data, response)
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
        let (data, response) = try await performAuthenticated(req)
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
        req.httpBody = try encoder.encode(body)
        let (data, response) = try await performAuthenticated(req, contentType: "application/json")
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
            let url = try router.unscopedURL(path: "/readyz")
            var req = URLRequest(url: url)
            req.httpMethod = "GET"
            req.timeoutInterval = 8
            req.setValue("no-store", forHTTPHeaderField: "Cache-Control")
            let (data, response) = try await session.data(for: req)
            let elapsed = start.duration(to: .now)
            let ms = Int(elapsed.components.seconds * 1000) + Int(elapsed.components.attoseconds / 1_000_000_000_000_000)
            try validateHTTPResponse(response, data: data)
            struct HealthBody: Decodable {
                let status: String
                let model: String?
            }
            let body = try decoder.decode(HealthBody.self, from: data)
            _ = try await ensureUserID()
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

    public func fetchAssistantSummary() async throws -> AssistantSummary {
        try await getJSON("/v1/assistant/summary")
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
        let toolsByServer = await withTaskGroup(
            of: (String, [MCPServerTool]?).self,
            returning: [String: [MCPServerTool]].self
        ) { group in
            for server in servers {
                let serverID = server.id
                group.addTask {
                    let tools = try? await self.fetchMCPTools(serverID: serverID)
                    return (serverID, tools)
                }
            }
            var result: [String: [MCPServerTool]] = [:]
            for await (serverID, tools) in group {
                if let tools {
                    result[serverID] = tools
                }
            }
            return result
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
                let initialBackoff = self.sseInitialBackoffSeconds
                let rolloverDelayMs = self.sseRolloverDelayMilliseconds
                var backoffSeconds = initialBackoff
                var retriedAuthOnCurrentCycle = false
                var forceNextTokenRefresh = false
                var hasEmittedInitialConnecting = false

                func updateCursor(for event: NoriStreamEvent) {
                    if case .reset(let pos) = event {
                        cursor = max(0, pos)
                    } else if let seq = event.sequence, seq > cursor {
                        cursor = seq
                    }
                }

                while !Task.isCancelled {
                    if !hasEmittedInitialConnecting {
                        hasEmittedInitialConnecting = true
                        continuation.yield(.connectionState(.connecting))
                    }

                    do {
                        let userID = try await self.ensureUserID()
                        let token = try await self.authProvider.validIDToken(forceRefresh: forceNextTokenRefresh)
                        forceNextTokenRefresh = false

                        let url = try self.router.scopedURL(
                            path: "/v1/events",
                            userID: userID,
                            queryItems: [URLQueryItem(name: "after", value: String(cursor))]
                        )
                        var req = URLRequest(url: url)
                        req.httpMethod = "GET"
                        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
                        req.setValue("no-cache", forHTTPHeaderField: "Cache-Control")
                        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")

                        let (bytes, response) = try await self.streamSession.bytes(for: req)
                        guard let http = response as? HTTPURLResponse else {
                            throw NoriAPIError(message: "Invalid event stream response.", statusCode: 0, retryable: true)
                        }

                        if http.statusCode == 401 {
                            if !retriedAuthOnCurrentCycle {
                                retriedAuthOnCurrentCycle = true
                                forceNextTokenRefresh = true
                                continue
                            }
                            continuation.finish(
                                throwing: NoriAPIError(
                                    message: "Event stream unauthorized (401).",
                                    statusCode: 401,
                                    code: "unauthorized",
                                    retryable: false
                                )
                            )
                            return
                        }

                        if http.statusCode == 403 {
                            continuation.finish(
                                throwing: NoriAPIError(
                                    message: "Event stream forbidden (403).",
                                    statusCode: 403,
                                    code: "forbidden",
                                    retryable: false
                                )
                            )
                            return
                        }

                        guard (200..<300).contains(http.statusCode) else {
                            throw NoriAPIError(
                                message: "Event stream rejected (\(http.statusCode)).",
                                statusCode: http.statusCode,
                                retryable: http.statusCode >= 500 || http.statusCode == 429
                            )
                        }

                        // Connected cleanly — reset backoff and auth retry state.
                        backoffSeconds = initialBackoff
                        retriedAuthOnCurrentCycle = false
                        continuation.yield(.connectionState(.connected))

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
                                    updateCursor(for: event)
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
                                updateCursor(for: event)
                                continuation.yield(event)
                            }
                        }

                        if let trailing = parser.flush(),
                           let event = NoriStreamEvent.decode(from: trailing, decoder: jsonDecoder) {
                            updateCursor(for: event)
                            continuation.yield(event)
                        }

                        // Clean EOF (e.g. 150s server rollover): reconnect immediately without backoff penalty.
                        if !Task.isCancelled && rolloverDelayMs > 0 {
                            try? await Task.sleep(for: .milliseconds(rolloverDelayMs))
                        }
                    } catch is CancellationError {
                        break
                    } catch let apiErr as NoriAPIError where apiErr.statusCode == 401 || apiErr.statusCode == 403 {
                        continuation.finish(throwing: apiErr)
                        return
                    } catch {
                        if Task.isCancelled { break }
                        let transientState: ConnectionState
                        if let urlErr = error as? URLError,
                           [.notConnectedToInternet, .cannotFindHost, .dnsLookupFailed, .networkConnectionLost, .internationalRoamingOff, .dataNotAllowed].contains(urlErr.code) {
                            transientState = .offline
                        } else {
                            transientState = .reconnecting
                        }
                        continuation.yield(.connectionState(transientState))
                        if forceNextTokenRefresh {
                            // Refresh attempt itself failed transiently; allow retrying it after backoff.
                            retriedAuthOnCurrentCycle = false
                        }
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

    public func retryTurn(turnID: String?, messageID: String?) async throws -> Session {
        struct RetryBody: Encodable {
            let turn_id: String?
            let message_id: String?
        }
        return try await sendJSON(
            "/api/retry",
            method: "POST",
            body: RetryBody(turn_id: turnID, message_id: messageID)
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
        req.httpBody = data
        let (responseData, response) = try await performAuthenticated(req, contentType: mediaType)
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
        returnTo: String?,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        struct ActionVal: Encodable {
            let action: String
            let return_to: String?
        }
        struct ActionReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: ActionVal
        }
        let cleanReturnTo = (returnTo?.isEmpty == false) ? returnTo : nil
        return try await mutate(
            "/v1/mcp/\(serverID)/actions",
            requestID: requestID,
            body: ActionReq(
                request_id: requestID,
                expected_revision: expectedRevision,
                value: ActionVal(action: action, return_to: cleanReturnTo)
            )
        )
    }

    public func setMCPCredential(
        serverID: String,
        expectedRevision: Int,
        secret: String,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        _ = expectedRevision
        return try await mutate(
            "/v1/mcp/\(serverID)/credential",
            requestID: requestID,
            body: MCPCredentialInput(requestID: requestID, token: secret)
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
        returnTo: String?,
        requestID: String
    ) async throws -> MutationReceipt<Connection> {
        struct ActionVal: Encodable {
            let action: String
            let return_to: String?
        }
        struct ActionReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: ActionVal
        }
        let cleanReturnTo = (returnTo?.isEmpty == false) ? returnTo : nil
        return try await mutate(
            "/v1/connections/\(connectionID)/actions",
            requestID: requestID,
            body: ActionReq(
                request_id: requestID,
                expected_revision: expectedRevision,
                value: ActionVal(action: action, return_to: cleanReturnTo)
            )
        )
    }

    // MARK: - Library & Search

    public func fetchLibrary() async throws -> [LibraryItem] {
        let resp: ListResponse<LibraryItem> = try await getJSON("/v1/library")
        return resp.items
    }

    public func fetchLibraryItem(id: String) async throws -> LibraryItem {
        let encodedID = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        return try await getJSON("/v1/library/\(encodedID)")
    }

    public func fetchLibraryItemFile(id: String, format: String?) async throws -> LibraryFilePayload {
        let userID = try await ensureUserID()
        let encodedID = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        var query: [URLQueryItem] = []
        if let format, !format.isEmpty {
            query.append(URLQueryItem(name: "format", value: format))
        }
        let url = try router.scopedURL(path: "/v1/library/\(encodedID)/file", userID: userID, queryItems: query)
        var req = URLRequest(url: url)
        req.httpMethod = "GET"
        let (data, response) = try await performAuthenticated(req)
        try validateHTTPResponse(response, data: data)
        let http = response as? HTTPURLResponse
        let mediaType = http?.value(forHTTPHeaderField: "Content-Type") ?? "application/octet-stream"
        let filename = Self.parseContentDispositionFilename(http?.value(forHTTPHeaderField: "Content-Disposition"))
        return LibraryFilePayload(data: data, mediaType: mediaType, filename: filename)
    }

    private static func parseContentDispositionFilename(_ header: String?) -> String? {
        guard let header, !header.isEmpty else { return nil }
        for part in header.split(separator: ";") {
            let trimmed = part.trimmingCharacters(in: .whitespaces)
            let lower = trimmed.lowercased()
            if lower.hasPrefix("filename*=") {
                let raw = String(trimmed.dropFirst("filename*=".count)).trimmingCharacters(in: CharacterSet(charactersIn: "\""))
                if let range = raw.range(of: "''") {
                    let encoded = String(raw[range.upperBound...])
                    return encoded.removingPercentEncoding ?? encoded
                }
                return raw.removingPercentEncoding ?? raw
            }
            if lower.hasPrefix("filename=") {
                let raw = String(trimmed.dropFirst("filename=".count))
                    .trimmingCharacters(in: .whitespaces)
                    .trimmingCharacters(in: CharacterSet(charactersIn: "\""))
                if !raw.isEmpty { return raw }
            }
        }
        return nil
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
    ) async throws -> MutationReceipt<MemoryActionResource> {
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
    ) async throws -> MutationReceipt<MemoryLogEntryActionResource> {
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
    ) async throws -> MutationReceipt<WorkingMemoryActionResource> {
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

    public func fetchAutomation(id: String) async throws -> Automation {
        let encodedID = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        return try await getJSON("/v1/automations/\(encodedID)")
    }

    public func updateAutomation(
        id: String,
        expectedRevision: Int,
        value: AutomationEditable,
        requestID: String
    ) async throws -> MutationReceipt<Automation> {
        struct UpdateReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: AutomationEditable
        }
        let encodedID = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        return try await mutate(
            "/v1/automations/\(encodedID)",
            method: "PUT",
            requestID: requestID,
            body: UpdateReq(request_id: requestID, expected_revision: expectedRevision, value: value)
        )
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

    public func createPermissionRule(
        _ rule: PermissionRuleEditable,
        requestID: String
    ) async throws -> MutationReceipt<PermissionRule> {
        struct RuleReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: PermissionRuleEditable
        }
        return try await mutate(
            "/v1/permissions",
            requestID: requestID,
            body: RuleReq(request_id: requestID, expected_revision: 0, value: rule)
        )
    }

    public func revokePermissionRule(
        id: String,
        expectedRevision: Int,
        requestID: String
    ) async throws -> MutationReceipt<PermissionRule> {
        struct ActionReq: Encodable {
            let request_id: String
            let expected_revision: Int
            let value: [String: String]
        }
        return try await mutate(
            "/v1/permissions/\(id)/actions",
            requestID: requestID,
            body: ActionReq(request_id: requestID, expected_revision: expectedRevision, value: ["action": "revoke"])
        )
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
