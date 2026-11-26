import Testing
import NoriCore
import NoriDesign
@testable import NoriFeatures

@Suite("Interaction Card Status Pill Presentation")
struct InteractionCardPresentationTests {
    private func makeInteraction(
        kind: InteractionKind,
        status: InteractionStatus,
        expiresAt: String? = nil,
        resolutionKind: InteractionResolution.Kind? = nil
    ) -> Interaction {
        let payload: InteractionPayload
        switch kind {
        case .approval:
            payload = .approval(ApprovalPayload(
                summary: "Send update",
                effect: .sensitiveSend,
                account: ApprovalAccount(connectionID: "conn-1", label: "alex@example.com")
            ))
        case .question:
            payload = .question(QuestionPayload(input: .text))
        case .accountSelection:
            payload = .accountSelection(AccountSelectionPayload(purpose: "mail.send", eligibleConnectionIDs: ["conn-1"]))
        case .signIn:
            payload = .signIn(SignInPayload(integrationID: "google"))
        }
        return Interaction(
            id: "int-1",
            revision: 1,
            kind: kind,
            status: status,
            prompt: "Confirm action",
            payload: payload,
            expiresAt: expiresAt,
            resolution: resolutionKind.map { InteractionResolution(kind: $0, at: "2026-11-26T10:00:00Z") }
        )
    }

    @Test("Differentiates resolved pills across approved, denied, declined, not now, signed in, and answered states")
    func resolvedPillStates() {
        let approved = InteractionStatusPresentation.statusPill(
            for: makeInteraction(kind: .approval, status: .resolved, resolutionKind: .approve)
        )
        #expect(approved == InteractionStatusPill(label: "Approved", tone: .ok))

        let deniedApproval = InteractionStatusPresentation.statusPill(
            for: makeInteraction(kind: .approval, status: .resolved, resolutionKind: .decline)
        )
        #expect(deniedApproval == InteractionStatusPill(label: "Denied", tone: .neutral))

        let declinedSignIn = InteractionStatusPresentation.statusPill(
            for: makeInteraction(kind: .signIn, status: .resolved, resolutionKind: .decline)
        )
        #expect(declinedSignIn == InteractionStatusPill(label: "Not now", tone: .neutral))

        let declinedQuestion = InteractionStatusPresentation.statusPill(
            for: makeInteraction(kind: .question, status: .resolved, resolutionKind: .decline)
        )
        #expect(declinedQuestion == InteractionStatusPill(label: "Declined", tone: .neutral))

        let signedIn = InteractionStatusPresentation.statusPill(
            for: makeInteraction(kind: .signIn, status: .resolved, resolutionKind: .signedIn)
        )
        #expect(signedIn == InteractionStatusPill(label: "Signed in", tone: .ok))

        let answered = InteractionStatusPresentation.statusPill(
            for: makeInteraction(kind: .question, status: .resolved, resolutionKind: .answer)
        )
        #expect(answered == InteractionStatusPill(label: "Answered", tone: .ok))

        let expired = InteractionStatusPresentation.statusPill(
            for: makeInteraction(kind: .approval, status: .expired)
        )
        #expect(expired == InteractionStatusPill(label: "Expired", tone: .neutral))

        let superseded = InteractionStatusPresentation.statusPill(
            for: makeInteraction(kind: .approval, status: .superseded)
        )
        #expect(superseded == InteractionStatusPill(label: "Replaced", tone: .neutral))
    }

    @Test("Renders Waiting pill for pending interactions without expires_at and clock pill when expires_at is present")
    func pendingPillStates() {
        let waiting = InteractionStatusPresentation.statusPill(
            for: makeInteraction(kind: .approval, status: .pending, expiresAt: nil)
        )
        #expect(waiting == InteractionStatusPill(label: "Waiting", tone: .warning, systemImage: nil))

        let timed = InteractionStatusPresentation.statusPill(
            for: makeInteraction(kind: .approval, status: .pending, expiresAt: "2099-01-01T00:00:00Z")
        )
        #expect(timed.tone == .warning)
        #expect(timed.systemImage == "clock")
        #expect(!timed.label.isEmpty)
    }
}
