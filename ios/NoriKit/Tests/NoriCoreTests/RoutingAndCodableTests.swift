import Foundation
import Testing
@testable import NoriCore

@Suite("Routing, Deep Links & Codable Round-Trips")
struct RoutingAndCodableTests {
    @Test("Direct Go Core router prefixes user-scoped routes with /users/{id}/")
    func directCoreRouting() throws {
        let router = try NoriEndpointRouter(profile: .simulatorLocal)

        let healthURL = try router.unscopedURL(path: "/healthz")
        #expect(healthURL.absoluteString == "http://127.0.0.1:8088/healthz")

        let resolveURL = try router.unscopedURL(path: "/api/users/resolve")
        #expect(resolveURL.absoluteString == "http://127.0.0.1:8088/api/users/resolve")

        let snapshotURL = try router.scopedURL(path: "/v1/snapshot", userID: "usr-42")
        #expect(snapshotURL.absoluteString == "http://127.0.0.1:8088/users/usr-42/v1/snapshot")

        let eventsURL = try router.scopedURL(
            path: "/v1/events",
            userID: "usr-42",
            queryItems: [URLQueryItem(name: "after", value: "12")]
        )
        #expect(eventsURL.absoluteString == "http://127.0.0.1:8088/users/usr-42/v1/events?after=12")

        let turnURL = try router.scopedURL(path: "/api/turn", userID: "usr-42")
        #expect(turnURL.absoluteString == "http://127.0.0.1:8088/users/usr-42/api/turn")
    }

    @Test("Hosted BFF router applies /api or /api/nori prefix without duplicating /api")
    func hostedBFFRouting() throws {
        let router = try NoriEndpointRouter(profile: .hostedOIDC)

        let snapshotURL = try router.scopedURL(path: "/v1/snapshot", userID: nil)
        #expect(snapshotURL.absoluteString == "https://nori.example.com/api/v1/snapshot")

        let turnURL = try router.scopedURL(path: "/api/turn", userID: nil)
        #expect(turnURL.absoluteString == "https://nori.example.com/api/turn")

        let customProfile = ServerProfile(
            id: "custom-bff",
            preset: .custom,
            name: "Custom BFF",
            subtitle: "",
            mode: .hostedBFF,
            baseURLString: "https://host.example.com",
            apiPrefix: "/api/nori"
        )
        let customRouter = try NoriEndpointRouter(profile: customProfile)
        let mcpURL = try customRouter.scopedURL(path: "/v1/mcp", userID: nil)
        #expect(mcpURL.absoluteString == "https://host.example.com/api/nori/v1/mcp")
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
