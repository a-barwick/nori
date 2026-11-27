import Foundation
import CoreGraphics
import ImageIO
import UniformTypeIdentifiers
import Testing
@testable import NoriCore

@Suite("iOS Feedback Fixes Unit Tests")
struct FeedbackFixesTests {


    private func makeCGImage(width: Int, height: Int, withNoise: Bool = false) throws -> CGImage {
        let colorSpace = CGColorSpaceCreateDeviceRGB()
        var pixels = [UInt8](repeating: 0, count: width * height * 4)
        var state: UInt64 = 0x9E37_79B9_7F4A_7C15
        for y in 0..<height {
            for x in 0..<width {
                let idx = (y * width + x) * 4
                if withNoise {
                    state ^= state >> 12
                    state ^= state << 25
                    state ^= state >> 27
                    let v = state &* 0x2545_F491_4F6C_DD1D
                    pixels[idx] = UInt8(truncatingIfNeeded: v >> 8)
                    pixels[idx + 1] = UInt8(truncatingIfNeeded: v >> 16)
                    pixels[idx + 2] = UInt8(truncatingIfNeeded: v >> 24)
                    pixels[idx + 3] = 255
                } else {
                    pixels[idx] = UInt8((x * 255) / max(1, width - 1))
                    pixels[idx + 1] = UInt8((y * 255) / max(1, height - 1))
                    pixels[idx + 2] = 180
                    pixels[idx + 3] = 255
                }
            }
        }
        let provider = CGDataProvider(data: Data(pixels) as CFData)!
        guard let image = CGImage(
            width: width,
            height: height,
            bitsPerComponent: 8,
            bitsPerPixel: 32,
            bytesPerRow: width * 4,
            space: colorSpace,
            bitmapInfo: CGBitmapInfo(rawValue: CGImageAlphaInfo.premultipliedLast.rawValue),
            provider: provider,
            decode: nil,
            shouldInterpolate: false,
            intent: .defaultIntent
        ) else {
            throw NSError(domain: "FeedbackFixesTests", code: 1)
        }
        return image
    }

    private func encodeImage(_ image: CGImage, type: UTType, orientation: Int? = nil) -> Data? {
        let data = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(data as CFMutableData, type.identifier as CFString, 1, nil) else {
            return nil
        }
        var props: [CFString: Any] = [:]
        if let orientation {
            props[kCGImagePropertyOrientation] = orientation
        }
        CGImageDestinationAddImage(dest, image, props.isEmpty ? nil : (props as CFDictionary))
        guard CGImageDestinationFinalize(dest) else { return nil }
        return data as Data
    }


    @Test("Detects PNG, JPEG, WebP, HEIC/HEIF magic bytes accurately")
    func detectImageFormats() throws {
        let img = try makeCGImage(width: 16, height: 16)
        let pngData = try #require(encodeImage(img, type: .png))
        let jpegData = try #require(encodeImage(img, type: .jpeg))

        #expect(ImageUploadNormalizer.detectFormat(of: pngData) == .png)
        #expect(ImageUploadNormalizer.detectFormat(of: jpegData) == .jpeg)

        let fakeWebP = Data([0x52, 0x49, 0x46, 0x46, 0x10, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50, 0x56, 0x50, 0x38, 0x20])
        #expect(ImageUploadNormalizer.detectFormat(of: fakeWebP) == .webp)

        let fakeHEIC = Data([0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70, 0x68, 0x65, 0x69, 0x63, 0x00, 0x00, 0x00, 0x00])
        #expect(ImageUploadNormalizer.detectFormat(of: fakeHEIC) == .heic)

        #expect(ImageUploadNormalizer.detectFormat(of: Data("not an image".utf8)) == .unknown)
        #expect(ImageUploadNormalizer.detectFormat(of: Data()) == .unknown)
    }

    @Test("Preserves small PNG screenshot as real image/png with .png filename without transcoding")
    func preservesSmallPNG() throws {
        let img = try makeCGImage(width: 64, height: 64)
        let pngData = try #require(encodeImage(img, type: .png))

        let result = try ImageUploadNormalizer.normalize(
            data: pngData,
            suggestedFilename: "photo_1795590359.jpg"
        )
        #expect(result.wasTranscoded == false)
        #expect(result.mediaType == "image/png")
        #expect(result.filename == "photo_1795590359.png")
        #expect(result.data == pngData)
    }

    @Test("Transcodes HEIC or non-web image formats to real JPEG with image/jpeg and .jpg filename")
    func transcodesHEICAndTIFFToJPEG() throws {
        let img = try makeCGImage(width: 80, height: 60)

        let tiffData = try #require(encodeImage(img, type: .tiff))
        #expect(ImageUploadNormalizer.detectFormat(of: tiffData) == .other)
        let tiffNorm = try ImageUploadNormalizer.normalize(
            data: tiffData,
            suggestedFilename: "scan.tiff"
        )
        #expect(tiffNorm.wasTranscoded == true)
        #expect(tiffNorm.mediaType == "image/jpeg")
        #expect(tiffNorm.filename == "scan.jpg")
        #expect(ImageUploadNormalizer.detectFormat(of: tiffNorm.data) == .jpeg)

        if let heicData = encodeImage(img, type: .heic) {
            #expect(ImageUploadNormalizer.detectFormat(of: heicData) == .heic)
            let heicNorm = try ImageUploadNormalizer.normalize(
                data: heicData,
                suggestedFilename: "IMG_1234.HEIC"
            )
            #expect(heicNorm.wasTranscoded == true)
            #expect(heicNorm.mediaType == "image/jpeg")
            #expect(heicNorm.filename == "IMG_1234.jpg")
            #expect(ImageUploadNormalizer.detectFormat(of: heicNorm.data) == .jpeg)
        }
    }

    @Test("Downscales and compresses oversized PNG to fit cleanly under maxBytes as JPEG")
    func compressesOversizedPNGToJPEG() throws {
        let noisy = try makeCGImage(width: 600, height: 600, withNoise: true)
        let largePNG = try #require(encodeImage(noisy, type: .png))
        let limit = 40_000
        #expect(largePNG.count > limit)

        let normalized = try ImageUploadNormalizer.normalize(
            data: largePNG,
            suggestedFilename: "huge_screenshot.png",
            maxBytes: limit
        )
        #expect(normalized.wasTranscoded == true)
        #expect(normalized.mediaType == "image/jpeg")
        #expect(normalized.filename == "huge_screenshot.jpg")
        #expect(normalized.data.count <= limit)
        #expect(ImageUploadNormalizer.detectFormat(of: normalized.data) == .jpeg)
    }

    @Test("Bakes EXIF orientation into transcoded JPEG dimensions")
    func preservesOrientationOnTranscode() throws {
        let img = try makeCGImage(width: 100, height: 40)
        let tiffData = try #require(encodeImage(img, type: .tiff, orientation: 6))

        let normalized = try ImageUploadNormalizer.normalize(
            data: tiffData,
            suggestedFilename: "rotated.tiff"
        )
        let source = try #require(CGImageSourceCreateWithData(normalized.data as CFData, nil))
        let props = try #require(CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any])
        let width = props[kCGImagePropertyPixelWidth] as? Int
        let height = props[kCGImagePropertyPixelHeight] as? Int
        #expect(width == 40)
        #expect(height == 100)
    }

    @Test("Rejects empty and non-image payloads with clear errors")
    func rejectsInvalidImagePayloads() {
        #expect(throws: ImageUploadNormalizationError.emptyData) {
            try ImageUploadNormalizer.normalize(data: Data(), suggestedFilename: "empty.jpg")
        }
        #expect(throws: ImageUploadNormalizationError.unsupportedImageFormat) {
            try ImageUploadNormalizer.normalize(data: Data("hello world".utf8), suggestedFilename: "notes.txt")
        }
    }


    @Test("OperationProjection decodes native web_search wire payload, hides nil authority, and omits Confirmed on read-only ops")
    func workOperationProjectionAndBadges() throws {
        let json = """
        {
          "id": "task-1-ws-m1-1",
          "call_id": "ws_abc123",
          "name": "web_search",
          "effect_class": "read",
          "outcome": "confirmed",
          "revision": 1,
          "attempt": 1,
          "result_ref": "hist-1",
          "action": { "digest": "sha256:111", "text": "Searched: SF Thursday weather" },
          "action_summary": "Searched: SF Thursday weather",
          "created_at": "2026-11-25T06:49:10Z",
          "owner": { "type": "task", "id": "task-1" },
          "task_title": "Weather check"
        }
        """
        let op = try JSONDecoder().decode(OperationProjection.self, from: Data(json.utf8))
        #expect(op.isWebSearch == true)
        #expect(op.displaySummary == "Searched: SF Thursday weather")
        #expect(op.displayAuthorityLabel == nil)
        #expect(op.statusBadge == nil)

        let writeOp = OperationProjection(
            id: "op-write",
            name: "calendar.create_event",
            effectClass: .reversibleWrite,
            outcome: .confirmed,
            actionSummary: "Created calendar event",
            authoritySource: "user_request"
        )
        #expect(writeOp.displayAuthorityLabel == "you asked")
        #expect(writeOp.statusBadge == OperationBadge(label: "Confirmed", tone: .ok))

        let failedRead = OperationProjection(
            id: "op-fail",
            name: "web_search",
            effectClass: .read,
            outcome: .failed
        )
        #expect(failedRead.statusBadge == OperationBadge(label: "Failed", tone: .danger))
        #expect(failedRead.displaySummary == "Searched the web")
    }

    @Test("WorkActionEntry groups consecutive web searches cleanly while preserving single searches and writes")
    func workActionEntryGrouping() {
        let ops: [OperationProjection] = [
            OperationProjection(id: "ws-1", name: "web_search", effectClass: .read, outcome: .confirmed, actionSummary: "Searched: SF weather Thursday"),
            OperationProjection(id: "ws-2", name: "web_search", effectClass: .read, outcome: .confirmed, actionSummary: "Read page: weather.gov"),
            OperationProjection(id: "ws-3", name: "web_search", effectClass: .read, outcome: .confirmed, actionSummary: "Searched: SF hourly forecast"),
            OperationProjection(id: "cal-1", name: "calendar.create", effectClass: .reversibleWrite, outcome: .confirmed, actionSummary: "Added rain alert"),
            OperationProjection(id: "ws-4", name: "web_search", effectClass: .read, outcome: .confirmed, actionSummary: "Searched: umbrella nearby")
        ]

        let grouped = WorkActionEntry.group(ops)
        #expect(grouped.count == 3)

        if case .webSearchGroup(_, let searchOps) = grouped[0] {
            #expect(searchOps.count == 3)
            #expect(grouped[0].groupTitle == "Searched the web (3 queries)")
            #expect(grouped[0].groupBadge == nil)
        } else {
            Issue.record("Expected first entry to be a webSearchGroup")
        }

        if case .single(let calOp) = grouped[1] {
            #expect(calOp.id == "cal-1")
        } else {
            Issue.record("Expected second entry to be single calendar op")
        }

        if case .single(let trailingSearch) = grouped[2] {
            #expect(trailingSearch.id == "ws-4")
        } else {
            Issue.record("Expected trailing single web search to remain ungrouped")
        }
    }


    @Test("Plain-English Effect labels and internal Nori schedule warning/account filtering")
    func scheduleApprovalCardHelpers() {
        #expect(Effect.reversibleWrite.displayLabel == "Change")
        #expect(Effect.sensitiveSend.displayLabel == "Send")
        #expect(Effect.read.displayLabel == "Read")

        let internalSchedule = ApprovalPayload(
            summary: "Daily standup prep\nEvery weekday at 9:00 AM PST",
            toolID: "nori/assistant/automation-create",
            warning: "This modifies external records.",
            effect: .reversibleWrite,
            account: ApprovalAccount(connectionID: "", label: "")
        )
        #expect(internalSchedule.isScheduleCreation == true)
        #expect(internalSchedule.isInternalNoriSchedule == true)
        #expect(internalSchedule.hasAccountLabel == false)
        #expect(internalSchedule.displayWarning == nil)

        let externalSend = ApprovalPayload(
            summary: "Send Q4 summary",
            toolID: "gmail.send",
            warning: "This sends an external email.",
            effect: .sensitiveSend,
            account: ApprovalAccount(connectionID: "conn-1", label: "alex@example.com")
        )
        #expect(externalSend.isInternalNoriSchedule == false)
        #expect(externalSend.hasAccountLabel == true)
        #expect(externalSend.displayWarning == "This sends an external email.")
    }


    @Test("Decodes typed failed and superseded assistant messages from backend wire format")
    func decodeTypedErrorAndSupersededMessages() throws {
        let failedJSON = """
        {
          "id": "turn-1-assistant",
          "turn_id": "turn-1",
          "run_id": "",
          "role": "assistant",
          "status": "failed",
          "text": "Nori's model service is temporarily busy. Your message is saved — tap Retry to try again.",
          "parts": [{ "type": "text", "text": "Nori's model service is temporarily busy. Your message is saved — tap Retry to try again." }],
          "error": {
            "code": "provider_overloaded",
            "message": "Nori's model service is temporarily busy. Your message is saved — tap Retry to try again.",
            "retryable": true
          },
          "created_at": "2026-11-25T07:01:18Z"
        }
        """
        let failed = try JSONDecoder().decode(Message.self, from: Data(failedJSON.utf8))
        #expect(failed.status == .failed)
        #expect(failed.isFailed == true)
        #expect(failed.isSuperseded == false)
        #expect(failed.isRetryableError == true)
        #expect(failed.error?.code == "provider_overloaded")
        #expect(failed.displayErrorMessage == "Nori's model service is temporarily busy. Your message is saved — tap Retry to try again.")

        let nonRetryableJSON = """
        {
          "id": "turn-2-assistant",
          "role": "assistant",
          "status": "failed",
          "text": "This conversation is too long.",
          "error": {
            "code": "context_too_long",
            "message": "This conversation is too long.",
            "retryable": false
          },
          "created_at": "2026-11-25T07:02:00Z"
        }
        """
        let nonRetryable = try JSONDecoder().decode(Message.self, from: Data(nonRetryableJSON.utf8))
        #expect(nonRetryable.isFailed == true)
        #expect(nonRetryable.isRetryableError == false)

        let supersededJSON = """
        {
          "id": "turn-1-assistant",
          "role": "assistant",
          "status": "superseded",
          "text": "",
          "created_at": "2026-11-25T07:01:18Z"
        }
        """
        let superseded = try JSONDecoder().decode(Message.self, from: Data(supersededJSON.utf8))
        #expect(superseded.status == .superseded)
        #expect(superseded.isSuperseded == true)
        #expect(superseded.isFailed == false)
    }


    @Test("Decodes inline reminder confirmation part from backend wire shape")
    func decodeInlineReminderPart() throws {
        let msgJSON = """
        {
          "id": "msg-rem-1",
          "role": "assistant",
          "status": "completed",
          "text": "Reminder set · Wed, Nov 25 at 12:00 PM PST · Check supernino updates",
          "parts": [
            {
              "type": "reminder",
              "id": "auto_1234567890abcdef1234",
              "name": "Check supernino updates",
              "text": "Wed, Nov 25 at 12:00 PM PST",
              "scheduled_at": "2026-11-25T20:00:00Z",
              "fallback": "Reminder set · Wed, Nov 25 at 12:00 PM PST · Check supernino updates"
            }
          ],
          "created_at": "2026-11-25T06:55:25Z"
        }
        """
        let message = try JSONDecoder().decode(Message.self, from: Data(msgJSON.utf8))
        #expect(message.reminderParts.count == 1)
        let part = try #require(message.reminderParts.first)
        #expect(part.isReminder == true)
        #expect(part.id == "auto_1234567890abcdef1234")
        #expect(part.reminderTitle == "Check supernino updates")
        #expect(part.text == "Wed, Nov 25 at 12:00 PM PST")
        #expect(part.scheduledAt == "2026-11-25T20:00:00Z")
        #expect(part.fallback == "Reminder set · Wed, Nov 25 at 12:00 PM PST · Check supernino updates")
    }

    @Test("WorkStepEntry unifies operations and events chronologically, filters external changes, and strips duplicate headings")
    func workDetailRedesignHelpers() {
        let ops: [OperationProjection] = [
            OperationProjection(id: "c0", name: "capability_load", effectClass: .control, outcome: .confirmed, createdAt: "2026-11-26T09:15:00Z"),
            OperationProjection(id: "ws-1", name: "web_search", effectClass: .read, outcome: .confirmed, actionSummary: "Query 1", createdAt: "2026-11-26T09:15:10Z"),
            OperationProjection(id: "ws-2", name: "web_search", effectClass: .read, outcome: .confirmed, actionSummary: "Query 2", createdAt: "2026-11-26T09:15:20Z"),
            OperationProjection(id: "w1", name: "notes_append", effectClass: .reversibleWrite, outcome: .confirmed, actionSummary: "Updated grocery note", createdAt: "2026-11-26T09:16:00Z"),
            OperationProjection(id: "s1", name: "gmail_send", effectClass: .sensitiveSend, outcome: .uncertain, actionSummary: "Send email", createdAt: "2026-11-26T09:16:30Z")
        ]
        let events: [RunEvent] = [
            RunEvent(text: "web_search", createdAt: "2026-11-26T09:15:10Z"),
            RunEvent(text: "Updated grocery note", createdAt: "2026-11-26T09:16:00Z"),
            RunEvent(text: "Saved recipe to Artifacts", createdAt: "2026-11-26T09:17:00Z")
        ]

        let changes = WorkStepEntry.externalChanges(from: ops)
        #expect(changes.map(\.id) == ["w1", "s1"])

        let steps = WorkStepEntry.build(operations: ops, events: events)
        #expect(steps.count == 4)
        #expect(steps[0].title == "Searched the web (2 queries)")
        #expect(steps[1].title == "Updated grocery note")
        #expect(steps[2].title == "Send email")
        #expect(steps[2].status == .warning)
        #expect(steps[3].title == "Saved recipe to Artifacts")

        #expect(
            WorkStepEntry.stripLeadingMatchingHeading("### Overnight Focaccia\n\nMix flour and water.", title: "Overnight Focaccia")
                == "Mix flour and water."
        )
        #expect(
            WorkStepEntry.stripLeadingMatchingHeading("## Ingredients\n\n- 500g flour", title: "Overnight Focaccia")
                == "## Ingredients\n\n- 500g flour"
        )
        #expect(WorkStepEntry.budgetPresets(currentBudgetUSD: 1.0) == [2.0, 5.0, 10.0])
        #expect(WorkStepEntry.budgetPresets(currentBudgetUSD: 10.0) == [15.0, 20.0, 30.0])
        #expect(WorkStepEntry.isLongMarkdown("Short summary.") == false)
        #expect(WorkStepEntry.isLongMarkdown(String(repeating: "Line\n", count: 10)) == true)
    }
}
