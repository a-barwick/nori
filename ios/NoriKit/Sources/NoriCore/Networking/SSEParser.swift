import Foundation

// MARK: - Raw SSE Frame & Parser

public struct SSEFrame: Sendable, Hashable {
    public var id: String?
    public var event: String?
    public var data: String
    public var retryMs: Int?

    public var sequence: Int? {
        guard let id else { return nil }
        return Int(id.trimmingCharacters(in: .whitespaces))
    }

    public init(id: String? = nil, event: String? = nil, data: String, retryMs: Int? = nil) {
        self.id = id
        self.event = event
        self.data = data
        self.retryMs = retryMs
    }
}

/// Incremental Server-Sent Events parser compliant with the W3C EventSource spec
/// and Nori's `core/internal/api/stream.go` framing (`id:`, `event:`, `data:`, `: comment`).
public struct SSEParser: Sendable {
    private var currentID: String?
    private var currentEvent: String?
    private var dataLines: [String] = []
    private var currentRetry: Int?

    public init() {}

    /// Feed a single line (without trailing newline, or with optional trailing `\r`).
    /// Returns an `SSEFrame` when a blank line dispatches a non-empty event.
    public mutating func feedLine(_ rawLine: String) -> SSEFrame? {
        let line: String
        if rawLine.hasSuffix("\r") {
            line = String(rawLine.dropLast())
        } else {
            line = rawLine
        }

        if line.isEmpty {
            return flush()
        }

        // Comment / heartbeat (`: heartbeat`)
        if line.hasPrefix(":") {
            return nil
        }

        let field: String
        let value: String
        if let colonIndex = line.firstIndex(of: ":") {
            field = String(line[..<colonIndex])
            var afterColon = line.index(after: colonIndex)
            if afterColon < line.endIndex, line[afterColon] == " " {
                afterColon = line.index(after: afterColon)
            }
            value = String(line[afterColon...])
        } else {
            field = line
            value = ""
        }

        switch field {
        case "id":
            if !value.contains("\0") {
                currentID = value
            }
        case "event":
            currentEvent = value
        case "data":
            dataLines.append(value)
        case "retry":
            if let ms = Int(value.trimmingCharacters(in: .whitespaces)), ms >= 0 {
                currentRetry = ms
            }
        default:
            break
        }
        return nil
    }

    /// Feed an entire chunk of text (e.g. for unit tests or buffered reads).
    public mutating func feedChunk(_ chunk: String) -> [SSEFrame] {
        var frames: [SSEFrame] = []
        let normalized = chunk.replacingOccurrences(of: "\r\n", with: "\n")
        for line in normalized.split(separator: "\n", omittingEmptySubsequences: false) {
            if let frame = feedLine(String(line)) {
                frames.append(frame)
            }
        }
        return frames
    }

    public mutating func flush() -> SSEFrame? {
        guard !dataLines.isEmpty else {
            currentEvent = nil
            currentRetry = nil
            return nil
        }
        let frame = SSEFrame(
            id: currentID,
            event: currentEvent,
            data: dataLines.joined(separator: "\n"),
            retryMs: currentRetry
        )
        currentEvent = nil
        dataLines.removeAll(keepingCapacity: true)
        currentRetry = nil
        return frame
    }
}

// MARK: - Typed Nori SSE Events

public struct PresencePayload: Sendable, Codable, Hashable {
    public var paused: Bool?
    public var conversation: Conversation?

    public init(paused: Bool? = nil, conversation: Conversation? = nil) {
        self.paused = paused
        self.conversation = conversation
    }
}

public enum NoriStreamEvent: Sendable, Hashable {
    case connectionState(ConnectionState)
    case reset(position: Int)
    case message(seq: Int?, message: Message)
    case task(seq: Int?, run: Run)
    case result(seq: Int?, result: SavedResult)
    case readPosition(seq: Int?, readPosition: ReadPosition)
    case conversation(seq: Int?, presence: PresencePayload)
    case presence(PresencePayload)
    case memory(seq: Int?, ref: String?)
    case unknown(seq: Int?, kind: String)

    public var sequence: Int? {
        switch self {
        case .connectionState: return nil
        case .reset(let pos): return pos
        case .message(let seq, _): return seq
        case .task(let seq, _): return seq
        case .result(let seq, _): return seq
        case .readPosition(let seq, _): return seq
        case .conversation(let seq, _): return seq
        case .presence: return nil
        case .memory(let seq, _): return seq
        case .unknown(let seq, _): return seq
        }
    }

    public static func decode(from frame: SSEFrame, decoder: JSONDecoder = JSONDecoder()) -> NoriStreamEvent? {
        guard let data = frame.data.data(using: .utf8) else { return nil }
        let kind = frame.event ?? "message"
        let seq = frame.sequence

        switch kind {
        case "reset":
            struct ResetBody: Decodable { let position: Int }
            if let body = try? decoder.decode(ResetBody.self, from: data) {
                return .reset(position: body.position)
            }
            return .reset(position: seq ?? 0)
        case "message":
            struct MsgBody: Decodable { let message: Message }
            if let body = try? decoder.decode(MsgBody.self, from: data) {
                return .message(seq: seq, message: body.message)
            }
            return .unknown(seq: seq, kind: kind)
        case "task":
            struct TaskBody: Decodable { let run: Run }
            if let body = try? decoder.decode(TaskBody.self, from: data) {
                return .task(seq: seq, run: body.run)
            }
            return .unknown(seq: seq, kind: kind)
        case "result":
            struct ResultBody: Decodable { let result: SavedResult }
            if let body = try? decoder.decode(ResultBody.self, from: data) {
                return .result(seq: seq, result: body.result)
            }
            return .unknown(seq: seq, kind: kind)
        case "read_position":
            struct ReadPosBody: Decodable {
                let readPosition: ReadPosition
                enum CodingKeys: String, CodingKey { case readPosition = "read_position" }
            }
            if let body = try? decoder.decode(ReadPosBody.self, from: data) {
                return .readPosition(seq: seq, readPosition: body.readPosition)
            }
            return .unknown(seq: seq, kind: kind)
        case "conversation":
            if let presence = try? decoder.decode(PresencePayload.self, from: data) {
                return .conversation(seq: seq, presence: presence)
            }
            return .unknown(seq: seq, kind: kind)
        case "presence":
            if let presence = try? decoder.decode(PresencePayload.self, from: data) {
                return .presence(presence)
            }
            return .unknown(seq: seq, kind: kind)
        case "memory":
            struct MemBody: Decodable { let ref: String? }
            let body = try? decoder.decode(MemBody.self, from: data)
            return .memory(seq: seq, ref: body?.ref)
        default:
            return .unknown(seq: seq, kind: kind)
        }
    }
}
