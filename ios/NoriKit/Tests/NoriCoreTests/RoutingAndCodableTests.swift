import Foundation
import Testing
@testable import NoriCore

@Suite("Routing, Deep Links & Codable Round-Trips")
struct RoutingAndCodableTests {
    @Test("Live Core router prefixes user-scoped routes with /users/{id}/")
    func directCoreRouting() throws {
        let router = NoriEndpointRouter(baseURL: NoriConfig.serverURL)

        let readyURL = try router.unscopedURL(path: "/readyz")
        #expect(readyURL.absoluteString == "https://nori-core-8273801370.us-central1.run.app/readyz")

        let resolveURL = try router.unscopedURL(path: "/api/users/resolve")
        #expect(resolveURL.absoluteString == "https://nori-core-8273801370.us-central1.run.app/api/users/resolve")

        let snapshotURL = try router.scopedURL(path: "/v1/snapshot", userID: "usr-42")
        #expect(snapshotURL.absoluteString == "https://nori-core-8273801370.us-central1.run.app/users/usr-42/v1/snapshot")

        let eventsURL = try router.scopedURL(
            path: "/v1/events",
            userID: "usr-42",
            queryItems: [URLQueryItem(name: "after", value: "12")]
        )
        #expect(eventsURL.absoluteString == "https://nori-core-8273801370.us-central1.run.app/users/usr-42/v1/events?after=12")

        let turnURL = try router.scopedURL(path: "/api/turn", userID: "usr-42")
        #expect(turnURL.absoluteString == "https://nori-core-8273801370.us-central1.run.app/users/usr-42/api/turn")
    }

    @Test("Google OAuth PKCE builds reversed redirect scheme and validates state")
    func googleOAuthPKCE() throws {
        let clientID = "123456-abc.apps.googleusercontent.com"
        #expect(NoriConfig.reversedClientIDScheme(for: clientID) == "com.googleusercontent.apps.123456-abc")
        #expect(NoriConfig.googleRedirectURI(for: clientID) == "com.googleusercontent.apps.123456-abc:/oauth2redirect")

        let pkce = GoogleOAuthPKCE(clientID: clientID)
        let authURL = try pkce.authorizationURL()
        #expect(authURL.host == "accounts.google.com")
        #expect(authURL.query?.contains("code_challenge_method=S256") == true)

        let goodCallback = URL(string: "com.googleusercontent.apps.123456-abc:/oauth2redirect?code=xyz&state=\(pkce.state)")!
        #expect(try pkce.extractAuthorizationCode(from: goodCallback) == "xyz")
    }

    @Test("Parses nori:// deep links for tabs, sheets, subroutes, and OAuth")
    func deepLinkParsing() {
        #expect(NoriDeepLink.parse(URL(string: "nori://tab/work")!) == .tab(.work))
        #expect(NoriDeepLink.parse(URL(string: "nori://tab/apps")!) == .tab(.apps))
        #expect(NoriDeepLink.parse(URL(string: "nori://sheet/activity")!) == .activitySheet)
        #expect(NoriDeepLink.parse(URL(string: "nori://memory")!) == .memory)
        #expect(NoriDeepLink.parse(URL(string: "nori://work/task-1")!) == .workDetail("task-1"))
        if case .oauthCallback(let url) = NoriDeepLink.parse(URL(string: "nori://oauth/callback?code=abc&state=123")!) {
            #expect(url.query?.contains("code=abc") == true)
        } else {
            Issue.record("Expected oauthCallback deep link")
        }
    }

    @Test("Codable round-trip for Interaction variants, Session, MCPServer, Memory, Automation, Goal, Preferences")
    func codableRoundTrips() throws {
        let encoder = JSONEncoder()
        let decoder = JSONDecoder()

        let approval = Interaction(
            id: "int-a",
            revision: 2,
            kind: .approval,
            taskID: "t-1",
            status: .pending,
            prompt: "Send email?",
            payload: .approval(ApprovalPayload(
                summary: "Email body",
                effect: .sensitiveSend,
                account: ApprovalAccount(connectionID: "c-1", label: "alex@example.com"),
                recipients: ["procurement@acme.com"],
                materialEffects: ["Sends external email"]
            )),
            actionDigest: "sha256:abc"
        )
        let encodedApproval = try encoder.encode(approval)
        let decodedApproval = try decoder.decode(Interaction.self, from: encodedApproval)
        #expect(decodedApproval == approval)

        let question = Interaction(
            id: "int-q",
            revision: 1,
            kind: .question,
            status: .pending,
            prompt: "Pick calendar",
            payload: .question(QuestionPayload(input: .choice, options: [QuestionOption(id: "w", label: "Work")]))
        )
        let decodedQuestion = try decoder.decode(Interaction.self, from: try encoder.encode(question))
        #expect(decodedQuestion == question)

        let accountSel = Interaction(
            id: "int-acc",
            revision: 1,
            kind: .accountSelection,
            status: .pending,
            prompt: "Choose account",
            payload: .accountSelection(AccountSelectionPayload(purpose: "calendar.create", eligibleConnectionIDs: ["c-1", "c-2"], rememberAsDefault: true))
        )
        let decodedAcc = try decoder.decode(Interaction.self, from: try encoder.encode(accountSel))
        #expect(decodedAcc == accountSel)

        let signIn = Interaction(
            id: "int-si",
            revision: 1,
            kind: .signIn,
            status: .pending,
            prompt: "Sign in to Linear",
            payload: .signIn(SignInPayload(auth: .oauth, integrationID: "mcp", connectionID: "mcp-linear", authorizeURL: "https://linear.app/oauth"))
        )
        let decodedSignIn = try decoder.decode(Interaction.self, from: try encoder.encode(signIn))
        #expect(decodedSignIn == signIn)

        let receipt = MutationReceipt(
            requestID: "req-1",
            accepted: true,
            status: .applied,
            resource: Preferences(revision: 4, timezone: "America/Los_Angeles", budgets: BudgetPreferences(taskUSD: 1.5, monthlyUSD: 30.0))
        )
        let decodedReceipt = try decoder.decode(MutationReceipt<Preferences>.self, from: try encoder.encode(receipt))
        #expect(decodedReceipt == receipt)
    }
}
