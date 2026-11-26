import Foundation

public struct NoriEndpointRouter: Sendable, Hashable {
    public let baseURL: URL

    private static let pathSegmentAllowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "-._~"))

    public init(baseURL: URL = NoriConfig.serverURL) {
        self.baseURL = baseURL
    }

    public static func encodePathSegment(_ raw: String) -> String {
        raw.addingPercentEncoding(withAllowedCharacters: pathSegmentAllowed) ?? raw
    }

    public func unscopedURL(path: String, queryItems: [URLQueryItem] = []) throws -> URL {
        let normalizedPath = path.hasPrefix("/") ? path : "/\(path)"
        return try buildURL(path: normalizedPath, queryItems: queryItems)
    }

    public func scopedURL(path: String, userID: String, queryItems: [URLQueryItem] = []) throws -> URL {
        guard !userID.isEmpty else {
            throw NoriAPIError(message: "User ID has not been resolved.", statusCode: 401)
        }
        let normalizedPath = path.hasPrefix("/") ? path : "/\(path)"
        let encodedUser = Self.encodePathSegment(userID)
        return try buildURL(path: "/users/\(encodedUser)\(normalizedPath)", queryItems: queryItems)
    }

    private func buildURL(path: String, queryItems: [URLQueryItem]) throws -> URL {
        guard var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: true) else {
            throw NoriAPIError(message: "Could not construct URL from \(baseURL.absoluteString)")
        }
        let existing = components.percentEncodedPath.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        if existing.isEmpty {
            components.percentEncodedPath = path
        } else {
            components.percentEncodedPath = "/\(existing)\(path)"
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

private struct MutationEnvelope<Value: Encodable>: Encodable {
    let request_id: String
    let expected_revision: Int
    let value: Value
}

private struct EmptyBody: Encodable {}

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
    private let localEmail: String?
    private var resolvedUserID: String?
    private var identityGeneration: UInt64 = 0
    private var inFlightUserResolve: (generation: UInt64, task: Task<String, Error>)?

    public init(
        baseURL: URL = NoriConfig.serverURL,
        authProvider: AuthTokenProvider = AuthTokenProvider(),
        urlSession: URLSession? = nil,
        sseInitialBackoffSeconds: Double = 1.0,
        sseRolloverDelayMilliseconds: Int = 250,
        localEmail: String? = nil
    ) {
        self.baseURL = baseURL
        self.localEmail = localEmail
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
        identityGeneration &+= 1
        inFlightUserResolve?.task.cancel()
        inFlightUserResolve = nil
        resolvedUserID = nil
        NoriTelemetryRuntime.shared.setUser(id: nil)
    }

    private func currentIdentityGeneration() -> UInt64 {
        identityGeneration
    }

    private func encodeSegment(_ raw: String) -> String {
        NoriEndpointRouter.encodePathSegment(raw)
    }

    public func resolveUser(
        idToken: String,
        email: String? = nil,
        name: String? = nil
    ) async throws -> UserResolveOutcome {
        let url = try router.unscopedURL(path: "/api/users/resolve")
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        if !idToken.isEmpty {
            req.setValue("Bearer \(idToken)", forHTTPHeaderField: "Authorization")
        }
        var payload: [String: String] = [:]
        if let email, !email.isEmpty { payload["email"] = email }
        if let name, !name.isEmpty { payload["name"] = name }
        req.httpBody = try encoder.encode(payload)

        let (data, response) = try await session.data(for: req)
        guard let http = response as? HTTPURLResponse else {
            throw NoriAPIError(message: "Invalid response from server.", statusCode: 0, code: "invalid_response", retryable: true)
        }
        if (200..<300).contains(http.statusCode) {
            struct ResolveResponse: Decodable { let id: String }
            let decoded = try decoder.decode(ResolveResponse.self, from: data)
            return .resolved(userID: decoded.id)
        }
        let errorPayload = try? decoder.decode(APIErrorPayload.self, from: data)
        if http.statusCode == 403 {
            if errorPayload?.code == "invite_required" {
                return .inviteRequired(message: errorPayload?.error ?? "An invite code is required to access Nori.")
            }
            return .forbidden(message: errorPayload?.error ?? "This Google account is not authorized to access Nori.")
        }
        throw NoriAPIError(
            message: errorPayload?.error ?? "Nori could not check your account. Try again shortly.",
            statusCode: http.statusCode,
            code: errorPayload?.code,
            retryable: http.statusCode == 429 || http.statusCode >= 500
        )
    }

    public func redeemInvite(
        idToken: String,
        inviteCode: String,
        email: String? = nil,
        name: String? = nil
    ) async throws -> String {
        let trimmed = inviteCode.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            throw NoriAPIError(message: "Enter an invite code.", statusCode: 400, code: "invite_code_required", retryable: false)
        }
        let url = try router.unscopedURL(path: "/api/users/redeem")
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        if !idToken.isEmpty {
            req.setValue("Bearer \(idToken)", forHTTPHeaderField: "Authorization")
        }
        var payload: [String: String] = ["invite_code": trimmed]
        if let email, !email.isEmpty { payload["email"] = email }
        if let name, !name.isEmpty { payload["name"] = name }
        req.httpBody = try encoder.encode(payload)

        let (data, response) = try await session.data(for: req)
        guard let http = response as? HTTPURLResponse else {
            throw NoriAPIError(message: "Invalid response from server.", statusCode: 0, code: "invalid_response", retryable: true)
        }
        if (200..<300).contains(http.statusCode) {
            struct RedeemResponse: Decodable { let id: String }
            let decoded = try decoder.decode(RedeemResponse.self, from: data)
            return decoded.id
        }
        let errorPayload = try? decoder.decode(APIErrorPayload.self, from: data)
        throw NoriAPIError(
            message: errorPayload?.error ?? "Invalid invite code.",
            statusCode: http.statusCode,
            code: errorPayload?.code,
            retryable: http.statusCode == 429 || http.statusCode >= 500
        )
    }

    private func ensureUserID() async throws -> String {
        if let resolvedUserID { return resolvedUserID }
        if let existing = inFlightUserResolve, existing.generation == identityGeneration {
            return try await existing.task.value
        }
        let generation = identityGeneration
        let task = Task<String, Error> {
            try await self.performEnsureUserID(generation: generation)
        }
        inFlightUserResolve = (generation: generation, task: task)
        defer {
            if inFlightUserResolve?.generation == generation {
                inFlightUserResolve = nil
            }
        }
        return try await task.value
    }

    private func performEnsureUserID(generation: UInt64) async throws -> String {
        try await withRequestSpan(route: "/api/users/resolve", method: "POST") { span in
            let url = try router.unscopedURL(path: "/api/users/resolve")
            var req = URLRequest(url: url)
            req.httpMethod = "POST"
            let resolvePayload: [String: String] = localEmail.map { ["email": $0] } ?? [:]
            req.httpBody = try encoder.encode(resolvePayload)
            let data = try await authenticatedData(req, contentType: "application/json", span: span)
            struct ResolveResponse: Decodable { let id: String }
            let resolved = try decoder.decode(ResolveResponse.self, from: data)
            guard generation == identityGeneration, !Task.isCancelled else { throw CancellationError() }
            self.resolvedUserID = resolved.id
            NoriTelemetryRuntime.shared.setUser(id: resolved.id)
            NoriLogger.auth.info("user.resolved", attributes: ["nori.user.id": resolved.id], breadcrumb: true)
            return resolved.id
        }
    }

    private func withRequestSpan<T>(
        route: String,
        method: String,
        _ perform: (any NoriTelemetrySpan) async throws -> T
    ) async throws -> T {
        let telemetry = NoriTelemetryRuntime.shared
        let span = telemetry.startSpan(
            name: "\(method) \(route)",
            operation: "http.client",
            tags: ["http.route": route, "http.request.method": method]
        )
        return try await NoriTelemetryRuntime.$parentSpan.withValue(span) {
            do {
                let result = try await perform(span)
                span.finish(status: .ok)
                return result
            } catch {
                if Task.isCancelled || error is CancellationError || (error as? URLError)?.code == .cancelled {
                    span.finish(status: .cancelled)
                } else {
                    recordTransportError(error, route: route, method: method)
                    span.finish(status: .error)
                }
                throw error
            }
        }
    }

    private func authenticatedData(
        _ request: URLRequest,
        contentType: String? = nil,
        span: any NoriTelemetrySpan
    ) async throws -> Data {
        let (data, response) = try await performAuthenticated(request, contentType: contentType, traceContext: span.traceContext)
        if let http = response as? HTTPURLResponse {
            span.setTag("http.response.status_code", value: String(http.statusCode))
        }
        try validateHTTPResponse(response, data: data)
        return data
    }

    private nonisolated func applyTraceHeaders(to request: inout URLRequest, traceContext: NoriTraceContext, recordTrace: Bool = true) {
        if recordTrace { NoriTelemetryRuntime.shared.recordTraceID(traceContext.traceID) }
        request.setValue(traceContext.traceparentHeader, forHTTPHeaderField: "traceparent")
        request.setValue(traceContext.sentryTraceHeader, forHTTPHeaderField: "sentry-trace")
        request.setValue("ios", forHTTPHeaderField: "X-Nori-Client")
    }

    private func performAuthenticated(
        _ baseRequest: URLRequest,
        contentType: String? = nil,
        traceContext: NoriTraceContext
    ) async throws -> (Data, URLResponse) {
        let generation = identityGeneration
        var req = baseRequest
        req.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        if localEmail == nil {
            let token = try await authProvider.validIDToken(forceRefresh: false)
            guard generation == identityGeneration else { throw CancellationError() }
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if let contentType {
            req.setValue(contentType, forHTTPHeaderField: "Content-Type")
        }
        applyTraceHeaders(to: &req, traceContext: traceContext)
        let (data, response) = try await session.data(for: req)
        guard generation == identityGeneration else { throw CancellationError() }
        if localEmail == nil, let http = response as? HTTPURLResponse, http.statusCode == 401 {
            NoriLogger.auth.warning("auth.token_refresh_retry", attributes: ["http.status_code": "401"], remote: false)
            let freshToken = try await authProvider.validIDToken(forceRefresh: true)
            guard generation == identityGeneration else { throw CancellationError() }
            var retryReq = baseRequest
            retryReq.setValue("no-store", forHTTPHeaderField: "Cache-Control")
            retryReq.setValue("Bearer \(freshToken)", forHTTPHeaderField: "Authorization")
            if let contentType {
                retryReq.setValue(contentType, forHTTPHeaderField: "Content-Type")
            }
            applyTraceHeaders(to: &retryReq, traceContext: traceContext)
            let result = try await session.data(for: retryReq)
            guard generation == identityGeneration else { throw CancellationError() }
            return result
        }
        return (data, response)
    }

    private func recordTransportError(_ error: Error, route: String, method: String) {
        var tags = NoriErrorDiagnostics.tags(for: error)
        tags["http.route"] = route
        tags["http.request.method"] = method
        if error is DecodingError {
            NoriLogger.network.error("network.decode_failed", attributes: tags)
            NoriTelemetryRuntime.shared.captureError(error, category: "network.decode", tags: tags)
        } else if let apiError = error as? NoriAPIError {
            let status = apiError.statusCode
            guard status == 0 || status == 429 || status >= 500 else { return }
            NoriLogger.network.error("network.request_failed", attributes: tags)
            NoriTelemetryRuntime.shared.captureError(error, category: "network.http", tags: tags)
        } else {
            NoriLogger.network.warning("network.transport_error", attributes: tags)
        }
    }

    private func normalizedRoute(for path: String) -> String {
        var parts = path.split(separator: "/").map(String.init)
        guard parts.count > 2, parts[0] == "v1" else { return path }
        switch parts[1] {
        case "memory" where parts[2] == "logs":
            if parts.count > 4 {
                parts[3] = ":date"
                parts[4] = ":id"
            }
        case "memory" where parts[2] == "working":
            if parts.count > 3 { parts[3] = ":id" }
        case "memory" where parts[2] == "search":
            break
        case "settings" where parts.count > 3 && (parts[2] == "model-keys" || parts[2] == "home-connectors"):
            if parts[3] != "pairing" {
                parts[3] = ":id"
            }
        case "work", "interactions", "library", "memory", "automations", "permissions", "mutations", "mcp", "connections":
            parts[2] = ":id"
        default:
            break
        }
        return "/" + parts.joined(separator: "/")
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
                    requestID: payload.requestID,
                    current: payload.current
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
        try await withRequestSpan(route: normalizedRoute(for: path), method: "GET") { span in
            let userID = try await ensureUserID()
            span.setTag("nori.user.id", value: userID)
            let url = try router.scopedURL(path: path, userID: userID, queryItems: queryItems)
            let data = try await authenticatedData(URLRequest(url: url), span: span)
            return try decoder.decode(T.self, from: data)
        }
    }

    private func sendJSON<Body: Encodable, Response: Decodable>(
        _ path: String,
        method: String = "POST",
        body: Body
    ) async throws -> Response {
        try await withRequestSpan(route: normalizedRoute(for: path), method: method) { span in
            let userID = try await ensureUserID()
            span.setTag("nori.user.id", value: userID)
            let url = try router.scopedURL(path: path, userID: userID)
            var req = URLRequest(url: url)
            req.httpMethod = method
            req.httpBody = try encoder.encode(body)
            let data = try await authenticatedData(req, contentType: "application/json", span: span)
            return try decoder.decode(Response.self, from: data)
        }
    }

    private func deleteJSON<Response: Decodable>(_ path: String) async throws -> Response {
        try await withRequestSpan(route: normalizedRoute(for: path), method: "DELETE") { span in
            let userID = try await ensureUserID()
            span.setTag("nori.user.id", value: userID)
            let url = try router.scopedURL(path: path, userID: userID)
            var req = URLRequest(url: url)
            req.httpMethod = "DELETE"
            let data = try await authenticatedData(req, span: span)
            return try decoder.decode(Response.self, from: data)
        }
    }

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
            let encodedID = encodeSegment(requestID)
            if let receipt: MutationReceipt<Resource> = try? await getJSON("/v1/mutations/\(encodedID)") {
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

    private func mutateEnvelope<Value: Encodable, Resource: Sendable & Codable & Hashable>(
        _ path: String,
        method: String = "POST",
        requestID: String,
        expectedRevision: Int = 0,
        value: Value
    ) async throws -> MutationReceipt<Resource> {
        try await mutate(
            path,
            method: method,
            requestID: requestID,
            body: MutationEnvelope(request_id: requestID, expected_revision: expectedRevision, value: value)
        )
    }

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

    private func captureSnapshotDomain<T: Sendable>(
        _ operation: () async throws -> T
    ) async -> Result<T, Error> {
        do {
            return .success(try await operation())
        } catch {
            return .failure(error)
        }
    }

    private func isFatalSnapshotError(_ error: Error) -> Bool {
        guard let apiErr = error as? NoriAPIError else { return false }
        return apiErr.isAuthError || apiErr.code == "data_reset_pending" || apiErr.code == "account_delete_pending"
    }

    public func fetchFullSnapshot() async throws -> NoriFullSnapshot {
        let session = try await fetchSession(before: nil)

        async let capsResult = captureSnapshotDomain { try await self.fetchCapabilities() }
        async let workResult = captureSnapshotDomain { try await self.fetchWorkItems(bucket: nil) }
        async let interactionsResult = captureSnapshotDomain { try await self.fetchInteractions() }
        async let mcpResult = captureSnapshotDomain { try await self.fetchMCPServers() }
        async let connectionsResult = captureSnapshotDomain { try await self.fetchConnections() }
        async let libraryResult = captureSnapshotDomain { try await self.fetchLibrary() }
        async let memoriesResult = captureSnapshotDomain { try await self.fetchCoreMemories() }
        async let logsResult = captureSnapshotDomain { try await self.fetchMemoryLogs() }
        async let workingResult = captureSnapshotDomain { try await self.fetchWorkingMemory() }
        async let automationsResult = captureSnapshotDomain { try await self.fetchAutomations() }
        async let goalsResult = captureSnapshotDomain { try await self.fetchGoals() }
        async let permissionsResult = captureSnapshotDomain { try await self.fetchPermissions() }
        async let preferencesResult = captureSnapshotDomain { try await self.fetchPreferences() }
        async let modelKeysResult = captureSnapshotDomain { try await self.fetchModelKeys() }

        let (
            capsRes,
            workRes,
            interactionsRes,
            mcpRes,
            connectionsRes,
            libraryRes,
            memoriesRes
        ) = await (
            capsResult,
            workResult,
            interactionsResult,
            mcpResult,
            connectionsResult,
            libraryResult,
            memoriesResult
        )
        let (
            logsRes,
            workingRes,
            automationsRes,
            goalsRes,
            permissionsRes,
            preferencesRes,
            modelKeysRes
        ) = await (
            logsResult,
            workingResult,
            automationsResult,
            goalsResult,
            permissionsResult,
            preferencesResult,
            modelKeysResult
        )

        var partialErrors: [String: String] = [:]
        func unwrap<T>(_ result: Result<T, Error>, domain: String, fallback: T) throws -> (value: T, loaded: Bool) {
            switch result {
            case .success(let value):
                return (value, true)
            case .failure(let error):
                if isFatalSnapshotError(error) {
                    throw error
                }
                partialErrors[domain] = error.localizedDescription
                return (fallback, false)
            }
        }

        let caps = try unwrap(capsRes.map(Optional.some), domain: "capabilities", fallback: nil)
        let work = try unwrap(workRes, domain: "work", fallback: [])
        let interactions = try unwrap(interactionsRes, domain: "interactions", fallback: [])
        let mcp = try unwrap(mcpRes, domain: "mcp", fallback: [])
        let connections = try unwrap(connectionsRes, domain: "connections", fallback: [])
        let library = try unwrap(libraryRes, domain: "library", fallback: [])
        let memories = try unwrap(memoriesRes, domain: "core_memories", fallback: [])
        let logs = try unwrap(logsRes, domain: "memory_logs", fallback: [])
        let working = try unwrap(workingRes, domain: "working_memory", fallback: WorkingMemory())
        let automations = try unwrap(automationsRes, domain: "automations", fallback: [])
        let goals = try unwrap(goalsRes, domain: "goals", fallback: [])
        let permissions = try unwrap(permissionsRes, domain: "permissions", fallback: [])
        let preferences = try unwrap(preferencesRes, domain: "preferences", fallback: Preferences())
        let modelKeys = try unwrap(modelKeysRes, domain: "model_keys", fallback: ModelKeysResponse())

        let toolsByServer: [String: [MCPServerTool]]
        if mcp.loaded {
            toolsByServer = await withTaskGroup(
                of: (String, [MCPServerTool]?).self,
                returning: [String: [MCPServerTool]].self
            ) { group in
                for server in mcp.value {
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
        } else {
            toolsByServer = [:]
        }

        return NoriFullSnapshot(
            capabilities: caps.value,
            session: session,
            workItems: work.value,
            interactions: interactions.value,
            mcpServers: mcp.value,
            mcpTools: toolsByServer,
            connections: connections.value,
            libraryItems: library.value,
            coreMemories: memories.value,
            memoryLogs: logs.value,
            workingMemory: working.value,
            automations: automations.value,
            goals: goals.value,
            permissions: permissions.value,
            preferences: preferences.value,
            modelKeys: modelKeys.value,
            didLoadCapabilities: caps.loaded,
            didLoadWorkItems: work.loaded,
            didLoadInteractions: interactions.loaded,
            didLoadMCPServers: mcp.loaded,
            didLoadConnections: connections.loaded,
            didLoadLibraryItems: library.loaded,
            didLoadCoreMemories: memories.loaded,
            didLoadMemoryLogs: logs.loaded,
            didLoadWorkingMemory: working.loaded,
            didLoadAutomations: automations.loaded,
            didLoadGoals: goals.loaded,
            didLoadPermissions: permissions.loaded,
            didLoadPreferences: preferences.loaded,
            didLoadModelKeys: modelKeys.loaded,
            partialErrors: partialErrors
        )
    }

    public nonisolated func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                var cursor = max(0, position)
                let streamGeneration = await self.currentIdentityGeneration()
                let initialBackoff = self.sseInitialBackoffSeconds
                let rolloverDelayMs = self.sseRolloverDelayMilliseconds
                var backoffSeconds = initialBackoff
                var retriedAuthOnCurrentCycle = false
                var forceNextTokenRefresh = false
                var hasEmittedInitialConnecting = false
                var reportedReconnectFailure = false

                func updateCursor(for event: NoriStreamEvent) {
                    if case .reset(let pos) = event {
                        cursor = max(0, pos)
                    } else if let seq = event.sequence, seq > cursor {
                        cursor = seq
                    }
                }

                while !Task.isCancelled {
                    guard await self.currentIdentityGeneration() == streamGeneration else { break }
                    if !hasEmittedInitialConnecting {
                        hasEmittedInitialConnecting = true
                        continuation.yield(.connectionState(.connecting))
                    }

                    var traceID: String?
                    do {
                        let userID = try await self.ensureUserID()
                        guard await self.currentIdentityGeneration() == streamGeneration else { break }
                        let token: String?
                        if self.localEmail == nil {
                            token = try await self.authProvider.validIDToken(forceRefresh: forceNextTokenRefresh)
                        } else {
                            token = nil
                        }
                        guard await self.currentIdentityGeneration() == streamGeneration else { break }
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
                        if let token {
                            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
                        }
                        let sseTrace = NoriTraceContext.random(sampled: false)
                        traceID = sseTrace.traceID
                        self.applyTraceHeaders(to: &req, traceContext: sseTrace, recordTrace: false)

                        let (bytes, response) = try await self.streamSession.bytes(for: req)
                        guard await self.currentIdentityGeneration() == streamGeneration else { break }
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

                        backoffSeconds = initialBackoff
                        retriedAuthOnCurrentCycle = false
                        reportedReconnectFailure = false
                        NoriLogger.sse.info("sse.connected", attributes: ["cursor": String(cursor)], breadcrumb: true)
                        continuation.yield(.connectionState(.connected))

                        var parser = SSEParser()
                        let jsonDecoder = JSONDecoder()

                        var lineBuffer: [UInt8] = []
                        lineBuffer.reserveCapacity(1024)
                        for try await byte in bytes {
                            if Task.isCancelled { break }
                            if byte == 0x0A {
                                guard await self.currentIdentityGeneration() == streamGeneration else { break }
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

                        guard await self.currentIdentityGeneration() == streamGeneration else { break }

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

                        if !Task.isCancelled && rolloverDelayMs > 0 {
                            try? await Task.sleep(for: .milliseconds(rolloverDelayMs))
                        }
                    } catch is CancellationError {
                        break
                    } catch let apiErr as NoriAPIError where apiErr.statusCode == 401 || apiErr.statusCode == 403 {
                        guard await self.currentIdentityGeneration() == streamGeneration else { break }
                        continuation.finish(throwing: apiErr)
                        return
                    } catch {
                        if Task.isCancelled { break }
                        guard await self.currentIdentityGeneration() == streamGeneration else { break }
                        let transientState: ConnectionState
                        if let urlErr = error as? URLError,
                           [.notConnectedToInternet, .cannotFindHost, .dnsLookupFailed, .networkConnectionLost, .internationalRoamingOff, .dataNotAllowed].contains(urlErr.code) {
                            transientState = .offline
                        } else {
                            transientState = .reconnecting
                        }
                        continuation.yield(.connectionState(transientState))
                        var sseTags = NoriErrorDiagnostics.tags(for: error)
                        sseTags["sse.state"] = transientState.rawValue
                        sseTags["sse.backoff_s"] = String(Int(backoffSeconds))
                        sseTags["nori.trace.id"] = traceID
                        let report = !reportedReconnectFailure && backoffSeconds >= 4.0
                        NoriLogger.sse.warning("sse.reconnecting", attributes: sseTags, remote: report)
                        if report {
                            reportedReconnectFailure = true
                            NoriTelemetryRuntime.shared.captureError(error, category: "sse.reconnect", tags: sseTags)
                        }
                        if forceNextTokenRefresh {
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

    public func sendTurn(id: String, message: String, attachmentIDs: [String]) async throws -> Session {
        try await sendTurn(id: id, message: message, attachmentIDs: attachmentIDs, modelSelection: nil)
    }

    public func sendTurn(id: String, message: String, attachmentIDs: [String], modelSelection: ModelSelection?) async throws -> Session {
        struct TurnBody: Encodable {
            let id: String
            let message: String
            let attachments: [String]?
            let model: ModelSelection?
        }
        let activeModel = (modelSelection?.isEmpty == false) ? modelSelection : nil
        return try await sendJSON(
            "/api/turn",
            method: "POST",
            body: TurnBody(id: id, message: message, attachments: attachmentIDs.isEmpty ? nil : attachmentIDs, model: activeModel)
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
        try await withRequestSpan(route: "/v1/uploads", method: "POST") { span in
            let userID = try await ensureUserID()
            span.setTag("nori.user.id", value: userID)
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
            let response = try await authenticatedData(req, contentType: mediaType, span: span)
            return try decoder.decode(Attachment.self, from: response)
        }
    }

    public func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem] {
        var query: [URLQueryItem] = []
        if let bucket {
            query.append(URLQueryItem(name: "bucket", value: bucket.rawValue))
        }
        let resp: ListResponse<WorkItem> = try await getJSON("/v1/work", queryItems: query)
        return resp.items
    }

    public func fetchWorkDetail(taskID: String) async throws -> WorkDetail {
        try await getJSON("/v1/work/\(encodeSegment(taskID))")
    }

    public func fetchOperations(connectionID: String? = nil) async throws -> [OperationProjection] {
        var query: [URLQueryItem] = []
        if let connectionID, !connectionID.isEmpty {
            query.append(URLQueryItem(name: "connection_id", value: connectionID))
        }
        let resp: ListResponse<OperationProjection> = try await getJSON("/v1/operations", queryItems: query)
        return resp.items
    }

    public func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem> {
        try await mutateEnvelope(
            "/v1/work/\(encodeSegment(taskID))/actions",
            requestID: requestID,
            value: ["action": "stop"]
        )
    }

    public func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem> {
        struct ContinueValue: Encodable {
            let action: String
            let budget_usd: Double
        }
        return try await mutateEnvelope(
            "/v1/work/\(encodeSegment(taskID))/actions",
            requestID: requestID,
            value: ContinueValue(action: "continue", budget_usd: budgetUSD)
        )
    }

    public func fetchInteractions() async throws -> [Interaction] {
        let resp: ListResponse<Interaction> = try await getJSON("/v1/interactions")
        return resp.items
    }

    public func fetchInteraction(id: String) async throws -> Interaction {
        try await getJSON("/v1/interactions/\(encodeSegment(id))")
    }

    public func respondToInteraction(
        id: String,
        expectedRevision: Int,
        answer: InteractionAnswer,
        requestID: String
    ) async throws -> MutationReceipt<Interaction> {
        try await mutateEnvelope(
            "/v1/interactions/\(encodeSegment(id))/responses",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: answer.payloadDictionary
        )
    }

    public func fetchMCPServers() async throws -> [MCPServer] {
        let resp: ListResponse<MCPServer> = try await getJSON("/v1/mcp")
        return resp.items
    }

    public func fetchMCPTools(serverID: String) async throws -> [MCPServerTool] {
        let resp: ListResponse<MCPServerTool> = try await getJSON("/v1/mcp/\(encodeSegment(serverID))/tools")
        return resp.items
    }

    public func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer> {
        try await mutateEnvelope("/v1/mcp", requestID: requestID, value: input)
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
        let cleanReturnTo = (returnTo?.isEmpty == false) ? returnTo : nil
        return try await mutateEnvelope(
            "/v1/mcp/\(encodeSegment(serverID))/actions",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: ActionVal(action: action, return_to: cleanReturnTo)
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
            "/v1/mcp/\(encodeSegment(serverID))/credential",
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
        return try await mutateEnvelope(
            "/v1/mcp/\(encodeSegment(serverID))/actions",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: ExposureVal(action: "set_exposure", tool: toolName, exposure: exposure.rawValue)
        )
    }

    public func fetchConnections() async throws -> [Connection] {
        let resp: ListResponse<Connection> = try await getJSON("/v1/connections")
        return resp.items
    }

    public func fetchIntegrations() async throws -> [IntegrationSummary] {
        let resp: ListResponse<IntegrationSummary> = try await getJSON("/v1/integrations")
        return resp.items
    }

    public func fetchConnectionResources(connectionID: String) async throws -> [ConnectionResource] {
        let resp: ListResponse<ConnectionResource> = try await getJSON("/v1/connections/\(encodeSegment(connectionID))/resources")
        return resp.items
    }

    public func createConnection(integrationID: String, requestID: String) async throws -> MutationReceipt<Connection> {
        struct Value: Encodable {
            let integration_id: String
            let title: String
            let selected_resource_ids: [String]
        }
        return try await mutateEnvelope(
            "/v1/connections",
            requestID: requestID,
            value: Value(integration_id: integrationID, title: "", selected_resource_ids: [])
        )
    }

    public func startGoogleDrivePicker(connectionID: String, returnTo: String) async throws -> AuthAttempt {
        struct Request: Encodable { let return_to: String }
        return try await sendJSON(
            "/v1/connections/\(encodeSegment(connectionID))/google-drive/picker",
            body: Request(return_to: returnTo)
        )
    }

    public func performConnectionAction(
        connectionID: String,
        expectedRevision: Int,
        action: String,
        returnTo: String?,
        requestID: String
    ) async throws -> MutationReceipt<Connection> {
        try await performConnectionAction(
            connectionID: connectionID,
            expectedRevision: expectedRevision,
            action: action,
            returnTo: returnTo,
            capabilities: [],
            requestID: requestID
        )
    }

    public func performConnectionAction(
        connectionID: String,
        expectedRevision: Int,
        action: String,
        returnTo: String?,
        capabilities: [String],
        requestID: String
    ) async throws -> MutationReceipt<Connection> {
        struct ActionVal: Encodable {
            let action: String
            let return_to: String?
            let capabilities: [String]?
        }
        let cleanReturnTo = (returnTo?.isEmpty == false) ? returnTo : nil
        return try await mutateEnvelope(
            "/v1/connections/\(encodeSegment(connectionID))/actions",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: ActionVal(action: action, return_to: cleanReturnTo, capabilities: capabilities.isEmpty ? nil : capabilities)
        )
    }

    public func fetchLibrary() async throws -> [LibraryItem] {
        let resp: ListResponse<LibraryItem> = try await getJSON("/v1/library")
        return resp.items
    }

    public func fetchLibraryItem(id: String) async throws -> LibraryItem {
        try await getJSON("/v1/library/\(encodeSegment(id))")
    }

    public func fetchLibraryItemFile(id: String, format: String?) async throws -> LibraryFilePayload {
        try await withRequestSpan(route: "/v1/library/:id/file", method: "GET") { span in
            let userID = try await ensureUserID()
            span.setTag("nori.user.id", value: userID)
            var query: [URLQueryItem] = []
            if let format, !format.isEmpty {
                query.append(URLQueryItem(name: "format", value: format))
            }
            let url = try router.scopedURL(path: "/v1/library/\(encodeSegment(id))/file", userID: userID, queryItems: query)
            let (data, response) = try await performAuthenticated(URLRequest(url: url), traceContext: span.traceContext)
            let http = response as? HTTPURLResponse
            if let http { span.setTag("http.response.status_code", value: String(http.statusCode)) }
            try validateHTTPResponse(response, data: data)
            let mediaType = http?.value(forHTTPHeaderField: "Content-Type") ?? "application/octet-stream"
            let filename = Self.parseContentDispositionFilename(http?.value(forHTTPHeaderField: "Content-Disposition"))
            return LibraryFilePayload(data: data, mediaType: mediaType, filename: filename)
        }
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
        try await mutateEnvelope(
            "/v1/library/\(encodeSegment(id))/actions",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: ["action": "revise", "instruction": instruction]
        )
    }

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

    public func searchMemory(query: String, layer: String? = nil, limit: Int = 20) async throws -> MemorySearchResponse {
        var queryItems = [
            URLQueryItem(name: "q", value: query),
            URLQueryItem(name: "limit", value: String(limit))
        ]
        if let layer, !layer.isEmpty, layer != "all" {
            queryItems.append(URLQueryItem(name: "layer", value: layer))
        }
        return try await getJSON("/v1/memory/search", queryItems: queryItems)
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
        return try await mutateEnvelope(
            "/v1/memory",
            requestID: requestID,
            value: MemVal(text: text, section: section, about: about, pinned: pinned)
        )
    }

    public func performMemoryAction(
        id: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<MemoryActionResource> {
        try await mutateEnvelope(
            "/v1/memory/\(encodeSegment(id))/actions",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: ["action": action]
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
        return try await mutateEnvelope(
            "/v1/memory/logs/\(encodeSegment(date))/\(encodeSegment(entryID))/actions",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: ActionVal(action: action, pinned: action == "promote" ? pinned : nil)
        )
    }

    public func performWorkingMemoryAction(
        itemID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<WorkingMemoryActionResource> {
        try await mutateEnvelope(
            "/v1/memory/working/\(encodeSegment(itemID))/actions",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: ["action": action]
        )
    }

    public func fetchAutomations() async throws -> [Automation] {
        let resp: ListResponse<Automation> = try await getJSON("/v1/automations")
        return resp.items
    }

    public func fetchAutomation(id: String) async throws -> Automation {
        try await getJSON("/v1/automations/\(encodeSegment(id))")
    }

    public func updateAutomation(
        id: String,
        expectedRevision: Int,
        value: AutomationEditable,
        requestID: String
    ) async throws -> MutationReceipt<Automation> {
        try await mutateEnvelope(
            "/v1/automations/\(encodeSegment(id))",
            method: "PUT",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: value
        )
    }

    public func performAutomationAction(
        id: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<Automation> {
        try await mutateEnvelope(
            "/v1/automations/\(encodeSegment(id))/actions",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: ["action": action]
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
        try await mutateEnvelope("/v1/permissions", requestID: requestID, value: rule)
    }

    public func revokePermissionRule(
        id: String,
        expectedRevision: Int,
        requestID: String
    ) async throws -> MutationReceipt<PermissionRule> {
        try await mutateEnvelope(
            "/v1/permissions/\(encodeSegment(id))/actions",
            requestID: requestID,
            expectedRevision: expectedRevision,
            value: ["action": "revoke"]
        )
    }

    public func fetchPreferences() async throws -> Preferences {
        try await getJSON("/v1/preferences")
    }

    public func fetchAPICosts(days: Int) async throws -> APICostResponse {
        try await getJSON("/v1/settings/api-costs", queryItems: [URLQueryItem(name: "days", value: String(days))])
    }

    public func resetUserData(confirmation: String) async throws {
        try await sendAccountMaintenance(path: "/v1/settings/reset", confirmation: confirmation, resumePending: false)
    }

    public func resumePendingDataReset() async throws {
        try await sendAccountMaintenance(path: "/v1/settings/reset", confirmation: "RESET", resumePending: true)
    }

    public func deleteAccount(confirmation: String, resumePending: Bool) async throws {
        try await sendAccountMaintenance(path: "/v1/settings/account/delete", confirmation: confirmation, resumePending: resumePending)
    }

    private func sendAccountMaintenance(path: String, confirmation: String, resumePending: Bool) async throws {
        struct MaintenanceRequest: Encodable {
            let confirmation: String
            let resume_pending: Bool?
        }
        try await withRequestSpan(route: path, method: "POST") { span in
            let userID = try await ensureUserID()
            span.setTag("nori.user.id", value: userID)
            let url = try router.scopedURL(path: path, userID: userID)
            var request = URLRequest(url: url)
            request.httpMethod = "POST"
            request.timeoutInterval = 90
            request.httpBody = try encoder.encode(MaintenanceRequest(
                confirmation: confirmation,
                resume_pending: resumePending ? true : nil
            ))
            _ = try await authenticatedData(request, contentType: "application/json", span: span)
        }
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
            let onboarding_completed_at: String?
        }
        let val = PrefVal(
            timezone: preferences.timezone,
            locale: preferences.locale,
            working_hours: preferences.workingHours,
            quiet_hours: preferences.quietHours,
            default_destination_ids: preferences.defaultDestinationIDs,
            account_defaults: preferences.accountDefaults,
            budgets: preferences.budgets,
            input: preferences.input,
            onboarding_completed_at: preferences.onboardingCompletedAt
        )
        return try await mutateEnvelope(
            "/v1/preferences",
            method: "PUT",
            requestID: requestID,
            expectedRevision: preferences.revision,
            value: val
        )
    }

    // MARK: - Model API Keys (BYOK)

    public func fetchModelSettings() async throws -> ModelSettingsResponse {
        try await getJSON("/v1/settings/models")
    }

    public func saveModelSettings(_ request: SaveModelSettingsRequest) async throws -> ModelSettingsResponse {
        try await sendJSON("/v1/settings/models", method: "PUT", body: request)
    }

    public func fetchModelKeys() async throws -> ModelKeysResponse {
        try await getJSON("/v1/settings/model-keys")
    }

    public func saveModelKey(provider: String, apiKey: String) async throws -> ModelKeyProvider {
        try await sendJSON(
            "/v1/settings/model-keys/\(encodeSegment(provider))",
            method: "PUT",
            body: SaveModelKeyRequest(apiKey: apiKey)
        )
    }

    public func deleteModelKey(provider: String) async throws -> ModelKeyProvider {
        try await deleteJSON("/v1/settings/model-keys/\(encodeSegment(provider))")
    }

    public func fetchHomeConnectors() async throws -> HomeConnectorsResponse {
        try await getJSON("/v1/settings/home-connectors")
    }

    public func lookupHomePairing(_ code: String) async throws -> HomePairing {
        try await sendJSON("/v1/settings/home-connectors/pairing/lookup", body: HomePairingRequest(userCode: code))
    }

    public func approveHomePairing(_ code: String) async throws {
        try await sendWithoutResponse("/v1/settings/home-connectors/pairing/approve", method: "POST", body: HomePairingRequest(userCode: code))
    }

    public func testHomeConnector(_ id: String) async throws -> HomeConnectorTest {
        try await sendJSON("/v1/settings/home-connectors/\(encodeSegment(id))/test", body: EmptyBody())
    }

    public func revokeHomeConnector(_ id: String) async throws {
        try await sendWithoutResponse("/v1/settings/home-connectors/\(encodeSegment(id))", method: "DELETE", body: EmptyBody())
    }

    private func sendWithoutResponse<Body: Encodable>(_ path: String, method: String, body: Body) async throws {
        try await withRequestSpan(route: normalizedRoute(for: path), method: method) { span in
            let userID = try await ensureUserID()
            span.setTag("nori.user.id", value: userID)
            let url = try router.scopedURL(path: path, userID: userID)
            var request = URLRequest(url: url)
            request.httpMethod = method
            request.httpBody = try encoder.encode(body)
            _ = try await authenticatedData(request, contentType: "application/json", span: span)
        }
    }
}
