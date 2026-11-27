import Foundation
import Testing
@testable import NoriCore
@testable import NoriFeatures

@Suite("Proactivity UI Parity & Presentation")
struct ProactivityFeaturesTests {

    @Test("BriefItemStatus resolves pending, sent, and skipped states from live interactions")
    func briefItemStatusResolution() {
        let approvalPayload = InteractionPayload.approval(
            ApprovalPayload(
                summary: "Reply to Maya",
                effect: .sensitiveSend,
                account: ApprovalAccount(connectionID: "conn-1", label: "Gmail")
            )
        )
        let pending = Interaction(
            id: "int-pending",
            kind: .approval,
            status: .pending,
            prompt: "Send email",
            payload: approvalPayload
        )
        let approved = Interaction(
            id: "int-sent",
            kind: .approval,
            status: .resolved,
            prompt: "Send email",
            payload: approvalPayload,
            resolution: InteractionResolution(kind: .approve)
        )
        let declined = Interaction(
            id: "int-declined",
            kind: .approval,
            status: .resolved,
            prompt: "Send email",
            payload: approvalPayload,
            resolution: InteractionResolution(kind: .decline)
        )
        let expired = Interaction(
            id: "int-expired",
            kind: .approval,
            status: .expired,
            prompt: "Send email",
            payload: approvalPayload
        )
        let supersededByEdit = Interaction(
            id: "int-superseded-edit",
            kind: .approval,
            status: .superseded,
            prompt: "Send email",
            payload: approvalPayload,
            supersededBy: "int-sent"
        )
        let all = [pending, approved, declined, expired, supersededByEdit]

        #expect(BriefItemStatus.resolve(interactionID: "int-pending", interactions: all) == .pending)
        #expect(BriefItemStatus.resolve(interactionID: "int-unknown", interactions: all) == .pending)
        #expect(BriefItemStatus.resolve(interactionID: "int-sent", interactions: all) == .sent)
        #expect(BriefItemStatus.resolve(interactionID: "int-superseded-edit", interactions: all) == .sent)
        #expect(BriefItemStatus.resolve(interactionID: "int-declined", interactions: all) == .skipped)
        #expect(BriefItemStatus.resolve(interactionID: "int-expired", interactions: all) == .skipped)
    }

    @Test("ChatThreadUnit.build emits brief unit and NoriMessageBubble hides brief from non-text parts")
    @MainActor
    func chatThreadBriefUnit() {
        let briefPart = MessagePart(
            id: "brief-1",
            type: "brief",
            fallback: "Morning brief fallback",
            greeting: "Good morning, Austin",
            meta: "Fri Nov 27 · 58°F",
            items: [
                BriefItem(title: "Design Sync with Sam", trailing: "2:00 PM"),
                BriefItem(title: "Reply to Maya on Q4 pricing", interactionID: "int-1", actionLabel: "Review")
            ]
        )
        let msg = Message(
            id: "msg-brief-1",
            role: .assistant,
            text: "Morning brief fallback",
            parts: [briefPart]
        )

        #expect(NoriMessageBubble.visibleNonTextParts(in: msg).isEmpty)

        let units = ChatThreadUnit.build(from: [msg], pendingInteractions: [])
        #expect(units.count == 1)
        if case .brief(let unitMsg, let parts) = units.first {
            #expect(unitMsg.id == "msg-brief-1")
            #expect(parts.count == 1)
            #expect(parts[0].greeting == "Good morning, Austin")
        } else {
            Issue.record("Expected .brief ChatThreadUnit")
        }
    }

    @Test("ApprovalDraftParser extracts recipient, subject, and body and computes edited_fields only on change")
    func approvalDraftParser() {
        let interaction = Interaction(
            id: "int-mail-1",
            kind: .approval,
            status: .pending,
            prompt: "Send reply",
            payload: .approval(
                ApprovalPayload(
                    summary: "To: Maya Lin (maya@acme.co)\nSubject: Re: Q4 Pricing Tier\n\nHi Maya — looks good overall.",
                    toolID: "google/mail/mail-send",
                    effect: .sensitiveSend,
                    account: ApprovalAccount(connectionID: "conn-gmail", label: "austin@example.com"),
                    recipients: ["Maya Lin (maya@acme.co)"]
                )
            ),
            actionDigest: "sha256:abc"
        )

        let parsed = ApprovalDraftParser.parse(interaction: interaction)
        #expect(parsed.recipient == "Maya Lin (maya@acme.co)")
        #expect(parsed.subject == "Re: Q4 Pricing Tier")
        #expect(parsed.body == "Hi Maya — looks good overall.")

        // Unchanged -> nil editedFields
        let unchanged = ApprovalDraftParser.editedFields(
            initial: parsed,
            currentSubject: "Re: Q4 Pricing Tier",
            currentBody: "Hi Maya — looks good overall."
        )
        #expect(unchanged == nil)

        // Body edited -> only body populated
        let bodyEdited = ApprovalDraftParser.editedFields(
            initial: parsed,
            currentSubject: "Re: Q4 Pricing Tier",
            currentBody: "Hi Maya — let's lock $24k."
        )
        #expect(bodyEdited == EditedApprovalFields(subject: nil, body: "Hi Maya — let's lock $24k."))

        // Both edited -> both populated
        let bothEdited = ApprovalDraftParser.editedFields(
            initial: parsed,
            currentSubject: "Re: Q4 Pricing Tier (Final)",
            currentBody: "Hi Maya — let's lock $24k."
        )
        #expect(bothEdited == EditedApprovalFields(subject: "Re: Q4 Pricing Tier (Final)", body: "Hi Maya — let's lock $24k."))
    }

    @Test("ProactivityTimeFormatting formats quiet hours and morning brief labels cleanly")
    func proactivityTimeFormatting() {
        #expect(ProactivityTimeFormatting.formatClock("21:00") == "9:00 PM")
        #expect(ProactivityTimeFormatting.formatClock("07:30") == "7:30 AM")
        #expect(ProactivityTimeFormatting.formatClock("00:15") == "12:15 AM")
        #expect(ProactivityTimeFormatting.formatClock("12:00") == "12:00 PM")

        #expect(ProactivityTimeFormatting.quietHoursLabel(nil) == "Off")
        #expect(ProactivityTimeFormatting.quietHoursLabel(QuietHours(enabled: false, start: "21:00", end: "07:30")) == "Off")
        #expect(ProactivityTimeFormatting.quietHoursLabel(QuietHours(enabled: true, start: "21:00", end: "07:30")) == "9:00 PM – 7:30 AM")

        #expect(ProactivityTimeFormatting.morningBriefLabel(nil) == "8:00 AM")
        #expect(ProactivityTimeFormatting.morningBriefLabel(ProactivityPreferences(morningBriefTime: "07:45", morningBriefEnabled: true)) == "7:45 AM")
        #expect(ProactivityTimeFormatting.morningBriefLabel(ProactivityPreferences(morningBriefTime: "08:00", morningBriefEnabled: false)) == "Off")
    }

    @Test("AutomationTrigger.compactCadenceLabel matches calm single-line row grammar")
    func compactCadenceLabels() {
        let daily = AutomationTrigger(kind: "recurring", localStart: "2026-11-27T08:00:00", rrule: "FREQ=DAILY")
        #expect(daily.compactCadenceLabel == "Daily 8:00 AM")

        let beforeEvent = AutomationTrigger(kind: "event_relative", offsetSeconds: -900)
        #expect(beforeEvent.compactCadenceLabel == "15m before")

        let watch2h = AutomationTrigger(kind: "watch", minIntervalSeconds: 7200)
        #expect(watch2h.compactCadenceLabel == "Every 2h")

        let weekdays = AutomationTrigger(kind: "recurring", localStart: "2026-11-27T09:30:00", rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR")
        #expect(weekdays.compactCadenceLabel == "Weekdays 9:30 AM")
    }
}
