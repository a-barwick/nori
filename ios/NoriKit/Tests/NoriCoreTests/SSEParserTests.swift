import Foundation
import Testing
@testable import NoriCore

@Suite("SSE Parser & Stream Event Decoding")
struct SSEParserTests {
    @Test("Parses heartbeat comments, multi-line data, and sequence IDs")
    func parseFrames() {
        var parser = SSEParser()
        let raw = """
        : heartbeat

        id: 42
        event: message
        data: {"message":{"id":"m1","run_id":"r1","role":"assistant","text":"Hello","created_at":"2026-11-23T10:00:00Z"}}

        id: 43
        event: reset
        data: {"position":
        data: 100}

        """

        let frames = parser.feedChunk(raw)
        #expect(frames.count == 2)

        #expect(frames[0].id == "42")
        #expect(frames[0].sequence == 42)
        #expect(frames[0].event == "message")

        #expect(frames[1].id == "43")
        #expect(frames[1].event == "reset")
        #expect(frames[1].data == "{\"position\":\n100}")
    }

    @Test("Handles CRLF line endings cleanly")
    func parseCRLF() {
        var parser = SSEParser()
        let chunk = "id: 7\r\nevent: memory\r\ndata: {\"ref\":\"mem-1\"}\r\n\r\n"
        let frames = parser.feedChunk(chunk)
        #expect(frames.count == 1)
        let event = NoriStreamEvent.decode(from: frames[0])
        #expect(event == .memory(seq: 7, ref: "mem-1"))
    }

    @Test("Decodes typed message, task, presence, and reset events")
    func decodeTypedEvents() {
        let msgFrame = SSEFrame(
            id: "10",
            event: "message",
            data: #"{"message":{"id":"m9","run_id":"r2","role":"assistant","text":"Ready","created_at":"2026-11-23T10:01:00Z"}}"#
        )
        let msgEvent = NoriStreamEvent.decode(from: msgFrame)
        guard case .message(let seq, let message) = msgEvent else {
            Issue.record("Expected message event, got \(String(describing: msgEvent))")
            return
        }
        #expect(seq == 10)
        #expect(message.id == "m9")
        #expect(message.text == "Ready")

        let taskFrame = SSEFrame(
            id: "11",
            event: "task",
            data: #"{"run":{"id":"r2","status":"running","cancel_requested":false,"progress":"Checking","started_at":"2026-11-23T10:00:00Z"}}"#
        )
        let taskEvent = NoriStreamEvent.decode(from: taskFrame)
        guard case .task(let taskSeq, let run) = taskEvent else {
            Issue.record("Expected task event")
            return
        }
        #expect(taskSeq == 11)
        #expect(run.id == "r2")
        #expect(run.status == .running)

        let presenceFrame = SSEFrame(
            id: nil,
            event: "presence",
            data: #"{"paused":false,"conversation":{"pending":true,"processing":true,"input_ids":["i1"],"restart_count":0,"frozen":false,"revision":3,"consumed":2}}"#
        )
        let presenceEvent = NoriStreamEvent.decode(from: presenceFrame)
        guard case .presence(let payload) = presenceEvent else {
            Issue.record("Expected presence event")
            return
        }
        #expect(payload.paused == false)
        #expect(payload.conversation?.processing == true)

        let resetFrame = SSEFrame(id: nil, event: "reset", data: #"{"position":55}"#)
        #expect(NoriStreamEvent.decode(from: resetFrame) == .reset(position: 55))
    }
}
