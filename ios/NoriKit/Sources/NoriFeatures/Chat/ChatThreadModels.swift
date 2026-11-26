import SwiftUI
import NoriCore
import NoriDesign

struct ChatScrollMetrics: Equatable {
    let distanceFromBottom: CGFloat
    let contentHeight: CGFloat
}

enum BubblePosition {
    case single
    case first
    case middle
    case last

    static func forIndex(_ index: Int, count: Int) -> BubblePosition {
        if count <= 1 { return .single }
        if index == 0 { return .first }
        if index == count - 1 { return .last }
        return .middle
    }

    var userRadii: RectangleCornerRadii {
        let r = NoriRadius.bubble
        let p = NoriRadius.bubblePinched
        switch self {
        case .single:
            return RectangleCornerRadii(topLeading: r, bottomLeading: r, bottomTrailing: r, topTrailing: r)
        case .first:
            return RectangleCornerRadii(topLeading: r, bottomLeading: r, bottomTrailing: p, topTrailing: r)
        case .middle:
            return RectangleCornerRadii(topLeading: r, bottomLeading: r, bottomTrailing: p, topTrailing: p)
        case .last:
            return RectangleCornerRadii(topLeading: r, bottomLeading: r, bottomTrailing: r, topTrailing: p)
        }
    }
}

enum ChatThreadUnit: Identifiable {
    case separator(id: String, label: String)
    case page(message: Message, part: MessagePart)
    case reminder(message: Message, parts: [MessagePart])
    case group(id: String, isUser: Bool, messages: [Message])
    case error(Message)
    case interaction(Interaction)

    var id: String {
        switch self {
        case .separator(let id, _): return id
        case .page(let message, _): return "page-\(message.id)"
        case .reminder(let message, _): return "reminder-\(message.id)"
        case .group(let id, _, _): return id
        case .error(let message): return "error-\(message.id)"
        case .interaction(let item): return Self.interactionAnchorID(for: item.id)
        }
    }

    static func interactionAnchorID(for interactionID: String) -> String {
        "inline-interaction-\(interactionID)"
    }

    static func build(
        from messages: [Message],
        pendingInteractions: [Interaction]
    ) -> [ChatThreadUnit] {
        var datedInteractions: [(index: Int, date: Date, interaction: Interaction)] = []
        var undatedInteractions: [Interaction] = []

        for (idx, interaction) in pendingInteractions.enumerated() {
            if let rawDate = interaction.createdAt,
               let parsedDate = NoriDateFormatting.parse(rawDate) {
                datedInteractions.append((index: idx, date: parsedDate, interaction: interaction))
            } else {
                undatedInteractions.append(interaction)
            }
        }

        datedInteractions.sort { lhs, rhs in
            if lhs.date != rhs.date {
                return lhs.date < rhs.date
            }
            return lhs.index < rhs.index
        }

        var result: [ChatThreadUnit] = []
        var buffer: [Message] = []
        var interactionCursor = 0

        func flushBuffer() {
            guard let first = buffer.first else { return }
            result.append(.group(id: "grp-\(first.id)", isUser: first.isFromUser, messages: buffer))
            buffer = []
        }

        for msg in messages {
            if msg.isSuperseded {
                continue
            }

            if let msgDate = NoriDateFormatting.parse(msg.createdAt) {
                while interactionCursor < datedInteractions.count && datedInteractions[interactionCursor].date < msgDate {
                    flushBuffer()
                    result.append(.interaction(datedInteractions[interactionCursor].interaction))
                    interactionCursor += 1
                }
            }

            if let sep = msg.separatorLabel, !sep.isEmpty {
                flushBuffer()
                result.append(.separator(id: "sep-\(msg.id)", label: sep))
            }

            if msg.isFailed {
                flushBuffer()
                result.append(.error(msg))
                continue
            }

            let reminderParts = msg.reminderParts
            if !reminderParts.isEmpty {
                flushBuffer()
                result.append(.reminder(message: msg, parts: reminderParts))
                continue
            }

            if let pagePart = msg.pagePart {
                flushBuffer()
                result.append(.page(message: msg, part: pagePart))
                continue
            }

            if let last = buffer.last, last.isFromUser == msg.isFromUser {
                buffer.append(msg)
            } else {
                flushBuffer()
                buffer = [msg]
            }
        }

        flushBuffer()

        while interactionCursor < datedInteractions.count {
            result.append(.interaction(datedInteractions[interactionCursor].interaction))
            interactionCursor += 1
        }

        for interaction in undatedInteractions {
            result.append(.interaction(interaction))
        }

        return result
    }
}
