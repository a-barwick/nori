import Foundation
import Testing
@testable import NoriCore

@Suite("Personal Agent Computer models and interactions")
struct ComputerTests {
    @Test("ComputerStatus decodes wire payload and formats calm status labels")
    func decodesComputerStatusAndLabels() throws {
        let json = """
        {
          "enabled": true,
          "computer_id": "cmp_123",
          "provider": "sprites",
          "status": "sleeping",
          "baseline_checkpoint_id": "ckpt_0",
          "disk_used_bytes": 536870912,
          "disk_max_bytes": 1073741824,
          "checkpoints": [
            {"id": "ckpt_1", "label": "Before Python tools", "created_at": "2026-11-27T10:00:00Z"}
          ],
          "active_sessions": [],
          "last_active_at": "2026-11-27T10:05:00Z"
        }
        """
        let decoded = try JSONDecoder().decode(ComputerStatus.self, from: Data(json.utf8))
        #expect(decoded.enabled == true)
        #expect(decoded.computerID == "cmp_123")
        #expect(decoded.status == .sleeping)
        #expect(decoded.status.displayLabel == "Asleep")
        #expect(abs(decoded.diskUsageRatio - 0.5) < 0.001)
        #expect(decoded.checkpoints.count == 1)
        #expect(decoded.checkpoints[0].label == "Before Python tools")

        #expect(ComputerState.running.displayLabel == "Active")
        #expect(ComputerState.notProvisioned.displayLabel == "Ready to start")
        #expect(ComputerState.provisioning.displayLabel == "Starting…")
        #expect(ComputerState.error.displayLabel == "Needs attention")
    }

    @Test("computer_takeover interaction decodes payload and encodes resume/cancel answers")
    func decodesTakeoverInteractionAndEncodesAnswers() throws {
        let json = """
        {
          "id": "int_takeover_1",
          "revision": 2,
          "kind": "computer_takeover",
          "task_id": "run_abc",
          "status": "pending",
          "surface": "work",
          "prompt": "Sign in to Gmail on your computer so Nori can continue.",
          "payload": {
            "reason": "Gmail requires 2FA",
            "url": "https://mail.google.com/mail/u/0/",
            "computer_id": "cmp_123",
            "task_id": "run_abc"
          }
        }
        """
        let interaction = try JSONDecoder().decode(Interaction.self, from: Data(json.utf8))
        #expect(interaction.kind == .computerTakeover)
        guard case .computerTakeover(let payload) = interaction.payload else {
            Issue.record("Expected .computerTakeover payload")
            return
        }
        #expect(payload.reason == "Gmail requires 2FA")
        #expect(payload.url == "https://mail.google.com/mail/u/0/")
        #expect(payload.computerID == "cmp_123")

        let resumeAnswer = InteractionAnswer.resume(text: "Signed in")
        #expect(resumeAnswer.payloadDictionary["kind"] == .string("resume"))
        #expect(resumeAnswer.payloadDictionary["text"] == .string("Signed in"))

        let cancelAnswer = InteractionAnswer.cancel()
        #expect(cancelAnswer.payloadDictionary["kind"] == .string("cancel"))
        #expect(cancelAnswer.payloadDictionary["text"] == nil)
    }

    @Test("resolved computer_takeover interaction decodes resume and cancel resolution kinds")
    func decodesTakeoverResolutions() throws {
        let resumedJSON = """
        {
          "id": "int_takeover_2",
          "revision": 3,
          "kind": "computer_takeover",
          "status": "resolved",
          "prompt": "Complete login",
          "payload": {},
          "resolution": {"kind": "resume", "at": "2026-11-27T10:15:00Z"}
        }
        """
        let resumed = try JSONDecoder().decode(Interaction.self, from: Data(resumedJSON.utf8))
        #expect(resumed.resolution?.kind == .resume)

        let cancelledJSON = """
        {
          "id": "int_takeover_3",
          "revision": 3,
          "kind": "computer_takeover",
          "status": "resolved",
          "prompt": "Complete login",
          "payload": {},
          "resolution": {"kind": "cancel", "at": "2026-11-27T10:16:00Z"}
        }
        """
        let cancelled = try JSONDecoder().decode(Interaction.self, from: Data(cancelledJSON.utf8))
        #expect(cancelled.resolution?.kind == .cancel)
    }
}
