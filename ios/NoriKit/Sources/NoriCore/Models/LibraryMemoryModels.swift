import Foundation

// MARK: - Library & Search

public struct Provenance: Sendable, Codable, Hashable {
    public enum ContentKind: String, Sendable, Codable, Hashable {
        case generated
        case uploaded
        case copied
        case live

        public var displayLabel: String {
            switch self {
            case .generated: return "Made by Nori"
            case .uploaded: return "You uploaded this"
            case .copied: return "Saved copy"
            case .live: return "Live from the source"
            }
        }
    }

    public var content: ContentKind
    public var source: ResourceRef?
    public var sourceURL: String?
    public var retrievedAt: String?
    public var connectionStatus: ConnectionStatus?

    enum CodingKeys: String, CodingKey {
        case content
        case source
        case sourceURL = "source_url"
        case retrievedAt = "retrieved_at"
        case connectionStatus = "connection_status"
    }

    public init(
        content: ContentKind = .generated,
        source: ResourceRef? = nil,
        sourceURL: String? = nil,
        retrievedAt: String? = nil,
        connectionStatus: ConnectionStatus? = nil
    ) {
        self.content = content
        self.source = source
        self.sourceURL = sourceURL
        self.retrievedAt = retrievedAt
        self.connectionStatus = connectionStatus
    }
}

public enum LibraryItemKind: String, Sendable, Codable, Hashable, CaseIterable {
    case answer
    case file
    case screenshot
    case link
    case document
    case event
    case message
    case report

    public var filterGroup: String {
        switch self {
        case .document, .report, .answer: return "documents"
        case .file, .screenshot, .link: return "files"
        case .event, .message: return "events"
        }
    }
}

public struct LibraryItem: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var rootID: String
    public var version: Int
    public var revises: String?
    public var supersededBy: String?
    public var kind: LibraryItemKind
    public var title: String
    public var name: String?
    public var mediaType: String?
    public var size: Int?
    public var url: String?
    public var taskID: String?
    public var conversationID: String?
    public var goalID: String?
    public var automationID: String?
    public var provenance: Provenance
    public var indexStatus: String?
    public var createdAt: String
    public var updatedAt: String?
    public var deletedAt: String?
    public var dataKind: String?
    public var data: AnyJSON?
    public var collection: String?
    public var previewText: String?
    public var deleteEffect: String?
    public var revision: Int
    /// Optional full markdown/text body (loaded for reader sheet or demo items).
    public var bodyText: String?

    enum CodingKeys: String, CodingKey {
        case id
        case rootID = "root_id"
        case version
        case revises
        case supersededBy = "superseded_by"
        case kind
        case title
        case name
        case mediaType = "media_type"
        case size
        case url
        case taskID = "task_id"
        case conversationID = "conversation_id"
        case goalID = "goal_id"
        case automationID = "automation_id"
        case provenance
        case indexStatus = "index_status"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case deletedAt = "deleted_at"
        case dataKind = "data_kind"
        case data
        case collection
        case previewText = "preview_text"
        case deleteEffect = "delete_effect"
        case revision
        case bodyText = "body_text"
    }

    public init(
        id: String,
        rootID: String? = nil,
        version: Int = 1,
        revises: String? = nil,
        supersededBy: String? = nil,
        kind: LibraryItemKind = .document,
        title: String,
        name: String? = nil,
        mediaType: String? = nil,
        size: Int? = nil,
        url: String? = nil,
        taskID: String? = nil,
        conversationID: String? = nil,
        goalID: String? = nil,
        automationID: String? = nil,
        provenance: Provenance = Provenance(),
        indexStatus: String? = "indexed",
        createdAt: String = ISO8601DateFormatter().string(from: Date()),
        updatedAt: String? = nil,
        deletedAt: String? = nil,
        dataKind: String? = nil,
        data: AnyJSON? = nil,
        collection: String? = nil,
        previewText: String? = nil,
        deleteEffect: String? = nil,
        revision: Int = 1,
        bodyText: String? = nil
    ) {
        self.id = id
        self.rootID = rootID ?? id
        self.version = version
        self.revises = revises
        self.supersededBy = supersededBy
        self.kind = kind
        self.title = title
        self.name = name
        self.mediaType = mediaType
        self.size = size
        self.url = url
        self.taskID = taskID
        self.conversationID = conversationID
        self.goalID = goalID
        self.automationID = automationID
        self.provenance = provenance
        self.indexStatus = indexStatus
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.deletedAt = deletedAt
        self.dataKind = dataKind
        self.data = data
        self.collection = collection
        self.previewText = previewText
        self.deleteEffect = deleteEffect
        self.revision = revision
        self.bodyText = bodyText
    }
}

public struct SearchJump: Sendable, Codable, Hashable {
    public var kind: String
    public var conversationID: String?
    public var messageID: String?
    public var taskID: String?
    public var itemID: String?
    public var goalID: String?
    public var automationID: String?
    public var layer: String?
    public var memoryID: String?
    public var logDate: String?
    public var entryID: String?

    enum CodingKeys: String, CodingKey {
        case kind
        case conversationID = "conversation_id"
        case messageID = "message_id"
        case taskID = "task_id"
        case itemID = "item_id"
        case goalID = "goal_id"
        case automationID = "automation_id"
        case layer
        case memoryID = "memory_id"
        case logDate = "log_date"
        case entryID = "entry_id"
    }

    public init(
        kind: String,
        conversationID: String? = nil,
        messageID: String? = nil,
        taskID: String? = nil,
        itemID: String? = nil,
        goalID: String? = nil,
        automationID: String? = nil,
        layer: String? = nil,
        memoryID: String? = nil,
        logDate: String? = nil,
        entryID: String? = nil
    ) {
        self.kind = kind
        self.conversationID = conversationID
        self.messageID = messageID
        self.taskID = taskID
        self.itemID = itemID
        self.goalID = goalID
        self.automationID = automationID
        self.layer = layer
        self.memoryID = memoryID
        self.logDate = logDate
        self.entryID = entryID
    }
}

public struct SearchHit: Sendable, Codable, Hashable, Identifiable {
    public enum Kind: String, Sendable, Codable, Hashable {
        case result
        case message
        case task
        case goal
        case automation
        case file
        case memory

        public var displayLabel: String {
            switch self {
            case .result: return "Document"
            case .message: return "Chat"
            case .task: return "Task"
            case .goal: return "Goal"
            case .automation: return "Automation"
            case .file: return "File"
            case .memory: return "Memory"
            }
        }
    }

    public var kind: Kind
    public var id: String
    public var title: String
    public var snippet: String
    public var jump: SearchJump?
    public var provenance: Provenance?
    public var freshness: String?
    public var score: Double?

    public init(
        kind: Kind,
        id: String,
        title: String,
        snippet: String,
        jump: SearchJump? = nil,
        provenance: Provenance? = nil,
        freshness: String? = nil,
        score: Double? = nil
    ) {
        self.kind = kind
        self.id = id
        self.title = title
        self.snippet = snippet
        self.jump = jump
        self.provenance = provenance
        self.freshness = freshness
        self.score = score
    }
}

public struct SearchResponse: Sendable, Codable, Hashable {
    public var items: [SearchHit]
    public var nextCursor: String?
    public var query: String?

    enum CodingKeys: String, CodingKey {
        case items
        case nextCursor = "next_cursor"
        case query
    }

    public init(items: [SearchHit], nextCursor: String? = nil, query: String? = nil) {
        self.items = items
        self.nextCursor = nextCursor
        self.query = query
    }
}

public struct LibraryFilePayload: Sendable, Hashable {
    public var data: Data
    public var mediaType: String
    public var filename: String?

    public var utf8Text: String? {
        String(data: data, encoding: .utf8)
    }

    public init(data: Data, mediaType: String, filename: String? = nil) {
        self.data = data
        self.mediaType = mediaType
        self.filename = filename
    }
}

public struct LibraryRevisionResult: Sendable, Hashable {
    public var status: MutationReceipt<LibraryItem>.Status
    public var taskID: String?
    public var item: LibraryItem?

    public var isQueued: Bool {
        status == .queued
    }

    public init(
        status: MutationReceipt<LibraryItem>.Status,
        taskID: String? = nil,
        item: LibraryItem? = nil
    ) {
        self.status = status
        self.taskID = taskID
        self.item = item
    }
}

// MARK: - Memory Models (3-Layer Architecture)

public enum MemoryProvenance: String, Sendable, Codable, Hashable {
    case userExplicit = "user_explicit"
    case preCompactFlush = "pre_compact_flush"
    case summaryEvicted = "summary_evicted"
    case taskOutcome = "task_outcome"
    case dreamCurated = "dream_curated"

    public var displayLabel: String {
        switch self {
        case .userExplicit: return "Explicit"
        case .preCompactFlush: return "Auto-saved"
        case .summaryEvicted: return "Auto-saved"
        case .taskOutcome: return "Task outcome"
        case .dreamCurated: return "Curated"
        }
    }
}

public struct MemoryOrigin: Sendable, Codable, Hashable {
    public var conversationID: String?
    public var messageID: String?
    public var taskID: String?

    enum CodingKeys: String, CodingKey {
        case conversationID = "conversation_id"
        case messageID = "message_id"
        case taskID = "task_id"
    }

    public init(conversationID: String? = nil, messageID: String? = nil, taskID: String? = nil) {
        self.conversationID = conversationID
        self.messageID = messageID
        self.taskID = taskID
    }
}

public struct MemoryFact: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var revision: Int
    public var text: String
    public var about: String?
    public var section: String?
    public var pinned: Bool
    public var provenance: MemoryProvenance?
    public var sourceText: String?
    public var supersedesID: String?
    public var scope: String
    public var origin: MemoryOrigin
    public var createdAt: String
    public var updatedAt: String?

    enum CodingKeys: String, CodingKey {
        case id
        case revision
        case text
        case about
        case section
        case pinned
        case provenance
        case sourceText = "source_text"
        case supersedesID = "supersedes_id"
        case scope
        case origin
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    public init(
        id: String,
        revision: Int = 1,
        text: String,
        about: String? = nil,
        section: String? = nil,
        pinned: Bool = false,
        provenance: MemoryProvenance? = .userExplicit,
        sourceText: String? = nil,
        supersedesID: String? = nil,
        scope: String = "user",
        origin: MemoryOrigin = MemoryOrigin(),
        createdAt: String = ISO8601DateFormatter().string(from: Date()),
        updatedAt: String? = nil
    ) {
        self.id = id
        self.revision = revision
        self.text = text
        self.about = about
        self.section = section
        self.pinned = pinned
        self.provenance = provenance
        self.sourceText = sourceText
        self.supersedesID = supersedesID
        self.scope = scope
        self.origin = origin
        self.createdAt = createdAt
        self.updatedAt = updatedAt
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.id = try c.decode(String.self, forKey: .id)
        self.revision = try c.decodeIfPresent(Int.self, forKey: .revision) ?? 1
        self.text = try c.decode(String.self, forKey: .text)
        self.about = try c.decodeIfPresent(String.self, forKey: .about)
        self.section = try c.decodeIfPresent(String.self, forKey: .section)
        self.pinned = try c.decodeIfPresent(Bool.self, forKey: .pinned) ?? false
        self.provenance = try c.decodeIfPresent(MemoryProvenance.self, forKey: .provenance)
        self.sourceText = try c.decodeIfPresent(String.self, forKey: .sourceText)
        self.supersedesID = try c.decodeIfPresent(String.self, forKey: .supersedesID)
        self.scope = try c.decodeIfPresent(String.self, forKey: .scope) ?? "user"
        self.origin = try c.decodeIfPresent(MemoryOrigin.self, forKey: .origin) ?? MemoryOrigin()
        self.createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt) ?? ""
        self.updatedAt = try c.decodeIfPresent(String.self, forKey: .updatedAt)
    }
}

public struct MemoryLogEntry: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var section: String?
    public var text: String
    public var about: String?
    public var provenance: MemoryProvenance
    public var sourceInputID: String?
    public var sourceText: String?
    public var runID: String?
    public var createdAt: String
    public var updatedAt: String?
    /// Client/demo convenience marker showing this log entry was promoted to Core Memory.
    public var promoted: Bool

    enum CodingKeys: String, CodingKey {
        case id
        case section
        case text
        case about
        case provenance
        case sourceInputID = "source_input_id"
        case sourceText = "source_text"
        case runID = "run_id"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case promoted
    }

    public init(
        id: String,
        section: String? = nil,
        text: String,
        about: String? = nil,
        provenance: MemoryProvenance = .preCompactFlush,
        sourceInputID: String? = nil,
        sourceText: String? = nil,
        runID: String? = nil,
        createdAt: String = ISO8601DateFormatter().string(from: Date()),
        updatedAt: String? = nil,
        promoted: Bool = false
    ) {
        self.id = id
        self.section = section
        self.text = text
        self.about = about
        self.provenance = provenance
        self.sourceInputID = sourceInputID
        self.sourceText = sourceText
        self.runID = runID
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.promoted = promoted
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.id = try c.decode(String.self, forKey: .id)
        self.section = try c.decodeIfPresent(String.self, forKey: .section)
        self.text = try c.decode(String.self, forKey: .text)
        self.about = try c.decodeIfPresent(String.self, forKey: .about)
        self.provenance = try c.decodeIfPresent(MemoryProvenance.self, forKey: .provenance) ?? .preCompactFlush
        self.sourceInputID = try c.decodeIfPresent(String.self, forKey: .sourceInputID)
        self.sourceText = try c.decodeIfPresent(String.self, forKey: .sourceText)
        self.runID = try c.decodeIfPresent(String.self, forKey: .runID)
        self.createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt) ?? ""
        self.updatedAt = try c.decodeIfPresent(String.self, forKey: .updatedAt)
        self.promoted = try c.decodeIfPresent(Bool.self, forKey: .promoted) ?? false
    }
}

public struct MemoryLog: Sendable, Codable, Hashable, Identifiable {
    public var id: String { date }
    public var date: String
    public var revision: Int
    public var entries: [MemoryLogEntry]
    public var updatedAt: String

    enum CodingKeys: String, CodingKey {
        case date
        case revision
        case entries
        case updatedAt = "updated_at"
    }

    public init(date: String, revision: Int = 1, entries: [MemoryLogEntry], updatedAt: String = "") {
        self.date = date
        self.revision = revision
        self.entries = entries
        self.updatedAt = updatedAt
    }
}

public enum WorkingMemoryItemKind: String, Sendable, Codable, Hashable {
    case constraint
    case correction
    case preference
    case approval
    case openQuestion = "open_question"
    case commitment
    case decision
    case fact

    public var displayLabel: String {
        switch self {
        case .constraint: return "Constraint"
        case .correction: return "Correction"
        case .preference: return "Preference"
        case .approval: return "Approval"
        case .openQuestion: return "Open question"
        case .commitment: return "Commitment"
        case .decision: return "Decision"
        case .fact: return "Fact"
        }
    }
}

public struct WorkingMemoryItem: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var kind: WorkingMemoryItemKind
    public var quote: String
    public var inputID: String
    public var note: String?

    enum CodingKeys: String, CodingKey {
        case id
        case kind
        case quote
        case inputID = "input_id"
        case note
    }

    public init(id: String, kind: WorkingMemoryItemKind, quote: String, inputID: String = "", note: String? = nil) {
        self.id = id
        self.kind = kind
        self.quote = quote
        self.inputID = inputID
        self.note = note
    }
}

public struct WorkingMemory: Sendable, Codable, Hashable {
    public var revision: Int
    public var from: String?
    public var extractedThrough: String?
    public var flushedThrough: String?
    public var through: String?
    public var dreamThrough: String?
    public var lastDreamAt: String?
    public var items: [WorkingMemoryItem]
    public var gist: String
    public var turns: Int
    public var model: String?
    public var coreBytes: Int
    public var coreBudgetBytes: Int
    public var updatedAt: String?

    enum CodingKeys: String, CodingKey {
        case revision
        case from
        case extractedThrough = "extracted_through"
        case flushedThrough = "flushed_through"
        case through
        case dreamThrough = "dream_through"
        case lastDreamAt = "last_dream_at"
        case items
        case gist
        case turns
        case model
        case coreBytes = "core_bytes"
        case coreBudgetBytes = "core_budget_bytes"
        case updatedAt = "updated_at"
    }

    public init(
        revision: Int = 1,
        from: String? = nil,
        extractedThrough: String? = nil,
        flushedThrough: String? = nil,
        through: String? = nil,
        dreamThrough: String? = nil,
        lastDreamAt: String? = nil,
        items: [WorkingMemoryItem] = [],
        gist: String = "",
        turns: Int = 0,
        model: String? = nil,
        coreBytes: Int = 0,
        coreBudgetBytes: Int = 24 * 1024,
        updatedAt: String? = nil
    ) {
        self.revision = revision
        self.from = from
        self.extractedThrough = extractedThrough
        self.flushedThrough = flushedThrough
        self.through = through
        self.dreamThrough = dreamThrough
        self.lastDreamAt = lastDreamAt
        self.items = items
        self.gist = gist
        self.turns = turns
        self.model = model
        self.coreBytes = coreBytes
        self.coreBudgetBytes = coreBudgetBytes
        self.updatedAt = updatedAt
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.revision = try c.decodeIfPresent(Int.self, forKey: .revision) ?? 1
        self.from = try c.decodeIfPresent(String.self, forKey: .from)
        self.extractedThrough = try c.decodeIfPresent(String.self, forKey: .extractedThrough)
        self.flushedThrough = try c.decodeIfPresent(String.self, forKey: .flushedThrough)
        self.through = try c.decodeIfPresent(String.self, forKey: .through)
        self.dreamThrough = try c.decodeIfPresent(String.self, forKey: .dreamThrough)
        self.lastDreamAt = try c.decodeIfPresent(String.self, forKey: .lastDreamAt)
        self.items = try c.decodeIfPresent([WorkingMemoryItem].self, forKey: .items) ?? []
        self.gist = try c.decodeIfPresent(String.self, forKey: .gist) ?? ""
        self.turns = try c.decodeIfPresent(Int.self, forKey: .turns) ?? 0
        self.model = try c.decodeIfPresent(String.self, forKey: .model)
        self.coreBytes = try c.decodeIfPresent(Int.self, forKey: .coreBytes) ?? 0
        self.coreBudgetBytes = try c.decodeIfPresent(Int.self, forKey: .coreBudgetBytes) ?? (24 * 1024)
        self.updatedAt = try c.decodeIfPresent(String.self, forKey: .updatedAt)
    }
}

// MARK: - Memory Mutation Receipt Payloads

public struct MemoryForgetResult: Sendable, Codable, Hashable {
    public var note: String?
    public var forgotten: MemoryFact?

    public init(note: String? = nil, forgotten: MemoryFact? = nil) {
        self.note = note
        self.forgotten = forgotten
    }
}

public struct MemoryDemoteResult: Sendable, Codable, Hashable {
    public var demoted: MemoryFact?
    public var logEntry: MemoryLogEntry?
    public var note: String?

    enum CodingKeys: String, CodingKey {
        case demoted
        case logEntry = "log_entry"
        case note
    }

    public init(demoted: MemoryFact? = nil, logEntry: MemoryLogEntry? = nil, note: String? = nil) {
        self.demoted = demoted
        self.logEntry = logEntry
        self.note = note
    }
}

/// Decodes `POST /v1/memory/{id}/actions` receipt `resource`:
/// - `pin` / `unpin` -> `MemoryFact`
/// - `forget` -> `{"note", "forgotten"}`
/// - `demote` -> `{"demoted", "log_entry", "note"}`
public enum MemoryActionResource: Sendable, Codable, Hashable {
    case fact(MemoryFact)
    case forgotten(MemoryForgetResult)
    case demoted(MemoryDemoteResult)

    public var fact: MemoryFact? {
        if case .fact(let fact) = self { return fact }
        return nil
    }

    public var forgetResult: MemoryForgetResult? {
        if case .forgotten(let result) = self { return result }
        return nil
    }

    public var demoteResult: MemoryDemoteResult? {
        if case .demoted(let result) = self { return result }
        return nil
    }

    private enum DiscriminatorKeys: String, CodingKey {
        case id
        case text
        case demoted
        case logEntry = "log_entry"
        case forgotten
        case note
    }

    public init(from decoder: any Decoder) throws {
        let keys = try decoder.container(keyedBy: DiscriminatorKeys.self)
        if keys.contains(.id), keys.contains(.text) {
            self = .fact(try MemoryFact(from: decoder))
        } else if keys.contains(.demoted) || keys.contains(.logEntry) {
            self = .demoted(try MemoryDemoteResult(from: decoder))
        } else {
            self = .forgotten(try MemoryForgetResult(from: decoder))
        }
    }

    public func encode(to encoder: any Encoder) throws {
        switch self {
        case .fact(let fact):
            try fact.encode(to: encoder)
        case .forgotten(let result):
            try result.encode(to: encoder)
        case .demoted(let result):
            try result.encode(to: encoder)
        }
    }
}

public struct MemoryLogEntryForgetResult: Sendable, Codable, Hashable {
    public var note: String?
    public var forgotten: MemoryLogEntry?

    public init(note: String? = nil, forgotten: MemoryLogEntry? = nil) {
        self.note = note
        self.forgotten = forgotten
    }
}

/// Decodes `POST /v1/memory/logs/{date}/{entry_id}/actions` receipt `resource`:
/// - `promote` -> `MemoryFact`
/// - `forget` -> `{"note", "forgotten"}`
public enum MemoryLogEntryActionResource: Sendable, Codable, Hashable {
    case promoted(MemoryFact)
    case forgotten(MemoryLogEntryForgetResult)
    case log(MemoryLog)

    public var promotedFact: MemoryFact? {
        if case .promoted(let fact) = self { return fact }
        return nil
    }

    public var forgetResult: MemoryLogEntryForgetResult? {
        if case .forgotten(let result) = self { return result }
        return nil
    }

    private enum DiscriminatorKeys: String, CodingKey {
        case id
        case text
        case date
        case entries
        case forgotten
        case note
    }

    public init(from decoder: any Decoder) throws {
        let keys = try decoder.container(keyedBy: DiscriminatorKeys.self)
        if keys.contains(.date), keys.contains(.entries) {
            self = .log(try MemoryLog(from: decoder))
        } else if keys.contains(.id), keys.contains(.text) {
            self = .promoted(try MemoryFact(from: decoder))
        } else {
            self = .forgotten(try MemoryLogEntryForgetResult(from: decoder))
        }
    }

    public func encode(to encoder: any Encoder) throws {
        switch self {
        case .promoted(let fact):
            try fact.encode(to: encoder)
        case .forgotten(let result):
            try result.encode(to: encoder)
        case .log(let log):
            try log.encode(to: encoder)
        }
    }
}

public struct WorkingMemoryDismissResult: Sendable, Codable, Hashable {
    public var dismissedItemID: String

    enum CodingKeys: String, CodingKey {
        case dismissedItemID = "dismissed_item_id"
    }

    public init(dismissedItemID: String) {
        self.dismissedItemID = dismissedItemID
    }
}

/// Decodes `POST /v1/memory/working/{item_id}/actions` receipt `resource`:
/// - `promote` -> `MemoryFact`
/// - `dismiss` -> `WorkingMemory` (when summary exists) or `{"dismissed_item_id"}`
public enum WorkingMemoryActionResource: Sendable, Codable, Hashable {
    case promoted(MemoryFact)
    case workingMemory(WorkingMemory)
    case dismissed(WorkingMemoryDismissResult)

    public var promotedFact: MemoryFact? {
        if case .promoted(let fact) = self { return fact }
        return nil
    }

    public var workingMemory: WorkingMemory? {
        if case .workingMemory(let wm) = self { return wm }
        return nil
    }

    public var dismissResult: WorkingMemoryDismissResult? {
        if case .dismissed(let result) = self { return result }
        return nil
    }

    private enum DiscriminatorKeys: String, CodingKey {
        case id
        case text
        case dismissedItemID = "dismissed_item_id"
    }

    public init(from decoder: any Decoder) throws {
        let keys = try decoder.container(keyedBy: DiscriminatorKeys.self)
        if keys.contains(.id), keys.contains(.text) {
            self = .promoted(try MemoryFact(from: decoder))
        } else if keys.contains(.dismissedItemID) {
            self = .dismissed(try WorkingMemoryDismissResult(from: decoder))
        } else {
            self = .workingMemory(try WorkingMemory(from: decoder))
        }
    }

    public func encode(to encoder: any Encoder) throws {
        switch self {
        case .promoted(let fact):
            try fact.encode(to: encoder)
        case .workingMemory(let wm):
            try wm.encode(to: encoder)
        case .dismissed(let result):
            try result.encode(to: encoder)
        }
    }
}
