import SwiftUI
import NoriCore
import NoriDesign

struct DocumentTextCard: View {
    let text: String
    let isLoadingFull: Bool
    let isMonospace: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if isLoadingFull {
                HStack(spacing: 6) {
                    ProgressView()
                        .controlSize(.mini)
                    Text("Loading full document…")
                        .font(NoriTypography.caption)
                        .foregroundStyle(NoriColors.ink3)
                }
            }
            if isMonospace {
                ScrollView(.horizontal, showsIndicators: true) {
                    Text(text)
                        .font(NoriTypography.monoSmall)
                        .foregroundStyle(NoriColors.ink)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .tint(NoriColors.accent)
                }
                .padding(14)
                .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
            } else {
                MarkdownBlocksView(markdown: text)
                    .textSelection(.enabled)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct DocumentStructuredDataCard: View {
    let dataKind: String
    let data: AnyJSON

    private var object: [String: AnyJSON]? {
        if case .object(let dict) = data { return dict }
        return nil
    }

    var body: some View {
        if let dict = object {
            switch dataKind {
            case "calendar_event":
                calendarEventCard(dict)
            case "mail_draft":
                mailDraftCard(dict)
            case "mail_message":
                mailMessageCard(dict)
            case "availability":
                availabilityCard(dict)
            default:
                EmptyView()
            }
        }
    }

    static func canRender(dataKind: String?, data: AnyJSON?) -> Bool {
        guard let dataKind, case .object = data else { return false }
        return ["calendar_event", "mail_draft", "mail_message", "availability"].contains(dataKind)
    }

    static func copyableText(dataKind: String?, data: AnyJSON?) -> String? {
        guard let dataKind, case .object(let dict) = data else { return nil }
        switch dataKind {
        case "mail_draft":
            let subject = stringValue(dict["subject"]) ?? ""
            let body = stringValue(dict["body_text"]) ?? ""
            let combined = [subject, body].filter { !$0.isEmpty }.joined(separator: "\n\n")
            return combined.isEmpty ? nil : combined
        case "mail_message":
            let subject = stringValue(dict["subject"]) ?? ""
            let content = objectValue(dict["content"]).flatMap { stringValue($0["text"]) } ?? ""
            let combined = [subject, content].filter { !$0.isEmpty }.joined(separator: "\n\n")
            return combined.isEmpty ? nil : combined
        case "calendar_event":
            let title = stringValue(dict["title"]) ?? ""
            let location = stringValue(dict["location"]) ?? ""
            let combined = [title, location].filter { !$0.isEmpty }.joined(separator: " · ")
            return combined.isEmpty ? nil : combined
        default:
            return nil
        }
    }

    private func calendarEventCard(_ dict: [String: AnyJSON]) -> some View {
        let title = Self.stringValue(dict["title"]) ?? "Event"
        let location = Self.stringValue(dict["location"])
        let timeSummary = Self.formatCalendarTime(Self.objectValue(dict["time"]))
        let attendees: [String] = Self.arrayValue(dict["attendees"]).compactMap { entry in
            guard case .object(let att) = entry else { return nil }
            if case .bool(true) = att["self"] { return nil }
            let label = Self.stringValue(att["name"]) ?? Self.stringValue(att["email"]) ?? ""
            guard !label.isEmpty else { return nil }
            switch Self.stringValue(att["response"]) {
            case "needs_action": return "\(label) (no reply)"
            case "declined": return "\(label) (declined)"
            default: return label
            }
        }
        let sourceURL: URL? = {
            guard let raw = Self.stringValue(dict["source_url"]),
                  let url = URL(string: raw),
                  url.scheme?.lowercased() == "https" else { return nil }
            return url
        }()

        return NoriCard(padding: 16) {
            VStack(alignment: .leading, spacing: 8) {
                Text(title)
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)

                let sub = [timeSummary, location].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
                if !sub.isEmpty {
                    Text(sub)
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink2)
                }

                if !attendees.isEmpty {
                    Text(attendees.joined(separator: ", "))
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink2)
                }

                if let sourceURL {
                    Link("Open in calendar", destination: sourceURL)
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(NoriColors.accent)
                        .padding(.top, 2)
                }
            }
        }
    }

    private func mailDraftCard(_ dict: [String: AnyJSON]) -> some View {
        let sender = Self.objectValue(dict["sender"]).flatMap { Self.stringValue($0["email"]) }
        let toList = Self.arrayValue(dict["to"]).compactMap { Self.objectValue($0).flatMap { Self.stringValue($0["email"]) } }
        let ccList = Self.arrayValue(dict["cc"]).compactMap { Self.objectValue($0).flatMap { Self.stringValue($0["email"]) } }
        let subject = Self.stringValue(dict["subject"]) ?? ""
        let bodyText = Self.stringValue(dict["body_text"]) ?? ""
        let state = Self.stringValue(dict["state"]) ?? "draft"

        return NoriCard(padding: 16) {
            VStack(alignment: .leading, spacing: 10) {
                Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 4) {
                    if let sender, !sender.isEmpty {
                        GridRow {
                            Text("From").font(NoriTypography.meta).foregroundStyle(NoriColors.ink3)
                            Text(sender).font(NoriTypography.meta).foregroundStyle(NoriColors.ink)
                        }
                    }
                    if !toList.isEmpty {
                        GridRow {
                            Text("To").font(NoriTypography.meta).foregroundStyle(NoriColors.ink3)
                            Text(toList.joined(separator: ", ")).font(NoriTypography.meta).foregroundStyle(NoriColors.ink)
                        }
                    }
                    if !ccList.isEmpty {
                        GridRow {
                            Text("Cc").font(NoriTypography.meta).foregroundStyle(NoriColors.ink3)
                            Text(ccList.joined(separator: ", ")).font(NoriTypography.meta).foregroundStyle(NoriColors.ink)
                        }
                    }
                }

                if !subject.isEmpty {
                    Text(subject)
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(NoriColors.ink)
                }

                if !bodyText.isEmpty {
                    Text(bodyText)
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                }

                NoriPill(state == "sent" ? "Sent" : "Draft", tone: state == "sent" ? .ok : .neutral)
            }
        }
    }

    private func mailMessageCard(_ dict: [String: AnyJSON]) -> some View {
        let fromObj = Self.objectValue(dict["from"])
        let fromLabel = fromObj.flatMap { Self.stringValue($0["name"]) ?? Self.stringValue($0["email"]) } ?? ""
        let receivedAt = Self.stringValue(dict["received_at"])
        let subject = Self.stringValue(dict["subject"]) ?? ""
        let contentObj = Self.objectValue(dict["content"])
        let contentText = contentObj.flatMap { Self.stringValue($0["text"]) } ?? ""
        let isSnippet = contentObj.flatMap { Self.stringValue($0["kind"]) } == "snippet"

        return NoriCard(padding: 16) {
            VStack(alignment: .leading, spacing: 8) {
                let header = [fromLabel, receivedAt.map { NoriDateFormatting.shortDateTime($0) }]
                    .compactMap { $0 }
                    .filter { !$0.isEmpty }
                    .joined(separator: " · ")
                if !header.isEmpty {
                    Text(header)
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink2)
                }
                if !subject.isEmpty {
                    Text(subject)
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(NoriColors.ink)
                }
                if !contentText.isEmpty {
                    Text(contentText)
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if isSnippet {
                    Text("Snippet only")
                        .font(NoriTypography.caption)
                        .foregroundStyle(NoriColors.ink3)
                }
            }
        }
    }

    private func availabilityCard(_ dict: [String: AnyJSON]) -> some View {
        let slots: [(start: String, status: String)] = Self.arrayValue(dict["slots"]).compactMap { entry in
            guard case .object(let slot) = entry,
                  let start = Self.stringValue(slot["start"]) else { return nil }
            let status = Self.stringValue(slot["status"]) ?? "unknown"
            return (start, status)
        }

        return NoriCard(padding: 16) {
            VStack(alignment: .leading, spacing: 8) {
                ForEach(Array(slots.enumerated()), id: \.offset) { _, slot in
                    let tone: NoriTone = slot.status == "free" ? .ok : (slot.status == "unknown" ? .warning : .neutral)
                    let label = slot.status.prefix(1).uppercased() + slot.status.dropFirst()
                    HStack {
                        Text(NoriDateFormatting.shortDateTime(slot.start))
                            .font(NoriTypography.body)
                            .foregroundStyle(NoriColors.ink)
                        Spacer(minLength: 8)
                        NoriPill(label, tone: tone)
                    }
                }
            }
        }
    }

    private static func stringValue(_ json: AnyJSON?) -> String? {
        if case .string(let s) = json { return s }
        return nil
    }

    private static func objectValue(_ json: AnyJSON?) -> [String: AnyJSON]? {
        if case .object(let o) = json { return o }
        return nil
    }

    private static func arrayValue(_ json: AnyJSON?) -> [AnyJSON] {
        if case .array(let a) = json { return a }
        return []
    }

    private static func formatCalendarTime(_ time: [String: AnyJSON]?) -> String? {
        guard let time else { return nil }
        let kind = stringValue(time["kind"])
        if kind == "all_day" {
            guard let startDate = stringValue(time["start_date"]) else { return nil }
            return "\(NoriDateFormatting.shortDate(startDate)) · all day"
        }
        if let start = stringValue(time["start"]) {
            if let end = stringValue(time["end"]) {
                return "\(NoriDateFormatting.shortDateTime(start)) – \(NoriDateFormatting.shortTime(end))"
            }
            return NoriDateFormatting.shortDateTime(start)
        }
        return nil
    }
}

struct DocumentImageCard: View {
    let image: DocumentPlatformImage
    let title: String
    let filename: String
    let canQuickLook: Bool
    let onQuickLook: () -> Void

    var body: some View {
        NoriCard(padding: 12) {
            VStack(alignment: .leading, spacing: 10) {
                Button(action: onQuickLook) {
                    DocumentFileUtilities.swiftUIImage(from: image)
                        .resizable()
                        .scaledToFit()
                        .frame(maxWidth: .infinity, maxHeight: 460)
                        .clipShape(RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        .overlay(
                            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                        )
                }
                .buttonStyle(.plain)
                .disabled(!canQuickLook)
                .accessibilityLabel("Preview \(title) full screen")

                HStack(spacing: 8) {
                    Text(filename)
                        .font(NoriTypography.caption)
                        .foregroundStyle(NoriColors.ink2)
                        .lineLimit(1)
                    Spacer(minLength: 8)
                    #if canImport(QuickLook)
                    if canQuickLook {
                        Button(action: onQuickLook) {
                            Label("Preview", systemImage: "arrow.up.left.and.arrow.down.right")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.accent)
                        }
                        .buttonStyle(.plain)
                    }
                    #endif
                }
            }
        }
    }
}

struct DocumentPDFCard: View {
    let thumbnail: DocumentPlatformImage?
    let filename: String
    let subtitle: String
    let canQuickLook: Bool
    let onQuickLook: () -> Void

    var body: some View {
        NoriCard(padding: 14) {
            VStack(alignment: .leading, spacing: 12) {
                if let thumbnail {
                    Button(action: onQuickLook) {
                        DocumentFileUtilities.swiftUIImage(from: thumbnail)
                            .resizable()
                            .scaledToFit()
                            .frame(maxWidth: .infinity, maxHeight: 380)
                            .background(Color.white)
                            .clipShape(RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                    .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                            )
                    }
                    .buttonStyle(.plain)
                    .disabled(!canQuickLook)
                    .accessibilityLabel("Open PDF preview")
                }

                HStack(spacing: 12) {
                    Image(systemName: "doc.richtext.fill")
                        .font(.system(size: 20, weight: .medium))
                        .foregroundStyle(NoriColors.accent)
                        .frame(width: 42, height: 42)
                        .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 11, style: .continuous))

                    VStack(alignment: .leading, spacing: 2) {
                        Text(filename)
                            .font(NoriTypography.rowTitle)
                            .foregroundStyle(NoriColors.ink)
                            .lineLimit(1)
                        Text(subtitle)
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink2)
                    }

                    Spacer(minLength: 8)
                }

                #if canImport(QuickLook)
                if canQuickLook {
                    Button(action: onQuickLook) {
                        Label("Preview", systemImage: "eye")
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                }
                #endif
            }
        }
    }
}

struct DocumentBinaryCard: View {
    let iconName: String
    let filename: String
    let subtitle: String
    let canQuickLook: Bool
    let onQuickLook: () -> Void

    var body: some View {
        NoriCard(padding: 16) {
            VStack(alignment: .leading, spacing: 14) {
                HStack(spacing: 12) {
                    Image(systemName: iconName)
                        .font(.system(size: 22, weight: .medium))
                        .foregroundStyle(NoriColors.accent)
                        .frame(width: 46, height: 46)
                        .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 12, style: .continuous))

                    VStack(alignment: .leading, spacing: 3) {
                        Text(filename)
                            .font(NoriTypography.rowTitle)
                            .foregroundStyle(NoriColors.ink)
                            .lineLimit(2)

                        Text(subtitle)
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink2)
                    }

                    Spacer(minLength: 8)
                }

                #if canImport(QuickLook)
                if canQuickLook {
                    Button(action: onQuickLook) {
                        Label("Preview", systemImage: "eye")
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                }
                #endif
            }
        }
    }
}
