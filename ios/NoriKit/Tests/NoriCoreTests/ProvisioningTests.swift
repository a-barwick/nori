import Foundation
import Testing
@testable import NoriCore

private final class ProvisioningURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (HTTPURLResponse, Data))?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let handler = Self.handler else {
            client?.urlProtocol(self, didFailWithError: URLError(.badServerResponse))
            return
        }
        do {
            let (response, data) = try handler(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

private func makeMockSession() -> URLSession {
    let config = URLSessionConfiguration.ephemeral
    config.protocolClasses = [ProvisioningURLProtocol.self]
    return URLSession(configuration: config)
}

private func makeJWT(payload: [String: Any]) -> String {
    let headerData = try! JSONSerialization.data(withJSONObject: ["alg": "RS256", "typ": "JWT"])
    let payloadData = try! JSONSerialization.data(withJSONObject: payload)
    func b64url(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
    return "\(b64url(headerData)).\(b64url(payloadData)).sig"
}

@Suite("Provisioning & Invite Redemption", .serialized)
struct ProvisioningTests {
    @Test("Extracts email and display name from Google ID token")
    func jwtClaimExtraction() {
        let jwt = makeJWT(payload: ["email": "  Alice@Example.COM ", "name": "  Alice Smith  "])
        #expect(GoogleOAuthPKCE.extractEmailFromJWT(jwt) == "alice@example.com")
        #expect(GoogleOAuthPKCE.extractNameFromJWT(jwt) == "Alice Smith")
    }

    @Test("Pre-exchanged PKCE returns cached session without network")
    func preexchangedPKCE() async throws {
        let pkce = GoogleOAuthPKCE(clientID: "test.apps.googleusercontent.com")
        let expected = AuthSession(idToken: "tok", refreshToken: "ref", expiresAt: .distantFuture, email: "alice@example.com")
        let wrapped = pkce.withExchangedSession(expected)
        let actual = try await wrapped.exchangeCode("unused-code", session: makeMockSession())
        #expect(actual == expected)
    }

    @Test("resolveUser surfaces invite_required, forbidden, and resolved outcomes")
    func resolveUserOutcomes() async throws {
        let baseURL = URL(string: "https://nori.example.test")!
        let client = NoriProvisioningClient(serverURL: baseURL, urlSession: makeMockSession())

        ProvisioningURLProtocol.handler = { req in
            #expect(req.url?.path == "/api/users/resolve")
            #expect(req.value(forHTTPHeaderField: "Authorization") == "Bearer id-tok")
            let resp = HTTPURLResponse(url: req.url!, statusCode: 403, httpVersion: nil, headerFields: nil)!
            let data = Data(#"{"error":"An invite code is required to access Nori.","code":"invite_required"}"#.utf8)
            return (resp, data)
        }
        let inviteOutcome = try await client.resolveUser(idToken: "id-tok", email: "alice@example.com", name: "Alice")
        #expect(inviteOutcome == .inviteRequired(message: "An invite code is required to access Nori."))

        ProvisioningURLProtocol.handler = { req in
            let resp = HTTPURLResponse(url: req.url!, statusCode: 403, httpVersion: nil, headerFields: nil)!
            let data = Data(#"{"error":"No user for this sign-in."}"#.utf8)
            return (resp, data)
        }
        let forbiddenOutcome = try await client.resolveUser(idToken: "id-tok", email: "alice@example.com")
        #expect(forbiddenOutcome == .forbidden(message: "No user for this sign-in."))

        ProvisioningURLProtocol.handler = { req in
            let resp = HTTPURLResponse(url: req.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
            let data = Data(#"{"id":"11111111-1111-4111-8111-111111111111"}"#.utf8)
            return (resp, data)
        }
        let resolvedOutcome = try await client.resolveUser(idToken: "id-tok", email: "alice@example.com")
        #expect(resolvedOutcome == .resolved(userID: "11111111-1111-4111-8111-111111111111"))
    }

    @Test("redeemInvite succeeds on 200 and surfaces structured error code on 403")
    func redeemInviteFlow() async throws {
        let baseURL = URL(string: "https://nori.example.test")!
        let client = NoriProvisioningClient(serverURL: baseURL, urlSession: makeMockSession())

        ProvisioningURLProtocol.handler = { req in
            #expect(req.url?.path == "/api/users/redeem")
            let resp = HTTPURLResponse(url: req.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
            let data = Data(#"{"id":"22222222-2222-4222-8222-222222222222"}"#.utf8)
            return (resp, data)
        }
        let userID = try await client.redeemInvite(idToken: "id-tok", inviteCode: "NORI-TEST-CODE", email: "alice@example.com", name: "Alice")
        #expect(userID == "22222222-2222-4222-8222-222222222222")

        ProvisioningURLProtocol.handler = { req in
            let resp = HTTPURLResponse(url: req.url!, statusCode: 403, httpVersion: nil, headerFields: nil)!
            let data = Data(#"{"error":"This invite code has already been used.","code":"invite_exhausted"}"#.utf8)
            return (resp, data)
        }
        do {
            _ = try await client.redeemInvite(idToken: "id-tok", inviteCode: "NORI-USED-CODE")
            Issue.record("expected redeemInvite to throw on 403")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 403)
            #expect(err.code == "invite_exhausted")
            #expect(err.message == "This invite code has already been used.")
        }
    }
}
