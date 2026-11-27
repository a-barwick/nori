import SwiftUI
import NoriCore
import NoriDesign

struct UserMessageBubble: View {
    let message: Message
    let position: BubblePosition
    let animateArrival: Bool
    let onCopy: (String) -> Void

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var hasAppeared: Bool

    init(
        message: Message,
        position: BubblePosition,
        animateArrival: Bool = false,
        onCopy: @escaping (String) -> Void = { _ in }
    ) {
        self.message = message
        self.position = position
        self.animateArrival = animateArrival
        self.onCopy = onCopy
        self._hasAppeared = State(initialValue: !animateArrival)
    }

    private var attachmentParts: [MessagePart] {
        (message.parts ?? []).filter { $0.type == "attachment" }
    }

    private var hasCopyableText: Bool {
        !message.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    var body: some View {
        HStack {
            Spacer(minLength: 52)
            VStack(alignment: .leading, spacing: 6) {
                if !message.text.isEmpty {
                    Text(message.text)
                        .font(NoriTypography.body)
                        .lineSpacing(2)
                        .foregroundStyle(NoriColors.onAccent)
                        .fixedSize(horizontal: false, vertical: true)
                }

                ForEach(attachmentParts) { part in
                    HStack(spacing: 6) {
                        Image(systemName: "doc.text")
                            .font(.system(size: 12, weight: .semibold))
                        Text(part.name ?? "Attachment")
                            .font(.system(size: 12, weight: .medium))
                            .lineLimit(1)
                    }
                    .foregroundStyle(NoriColors.onAccent)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 5)
                    .background(NoriColors.onAccent.opacity(0.16), in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .background(
                NoriColors.brandPrimary,
                in: UnevenRoundedRectangle(cornerRadii: position.userRadii, style: .continuous)
            )
            .modifier(UserBubbleContextShape(radii: position.userRadii))
            .contextMenu {
                if hasCopyableText {
                    Button {
                        onCopy(message.text)
                    } label: {
                        Label("Copy", systemImage: "doc.on.doc")
                    }
                }
            }
            .opacity(hasAppeared ? 1 : 0)
            .scaleEffect(hasAppeared || reduceMotion ? 1 : 0.94, anchor: .bottomTrailing)
            .offset(y: hasAppeared || reduceMotion ? 0 : 22)
            .onAppear {
                guard !hasAppeared else { return }
                let anim: Animation = reduceMotion
                    ? .easeOut(duration: 0.16)
                    : .spring(response: 0.34, dampingFraction: 0.78)
                withAnimation(anim) {
                    hasAppeared = true
                }
            }
        }
    }
}

private struct UserBubbleContextShape: ViewModifier {
    let radii: RectangleCornerRadii

    func body(content: Content) -> some View {
        #if os(iOS)
        content.contentShape(
            .contextMenuPreview,
            UnevenRoundedRectangle(cornerRadii: radii, style: .continuous)
        )
        #else
        content.contentShape(
            UnevenRoundedRectangle(cornerRadii: radii, style: .continuous)
        )
        #endif
    }
}

struct NoriMessageBubble: View {
    let message: Message
    let showAvatar: Bool
    let markState: MarkState
    let avatarNamespace: Namespace.ID
    let onCopy: (String) -> Void

    private var hasCopyableText: Bool {
        !message.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    static func visibleNonTextParts(in message: Message) -> [MessagePart] {
        (message.parts ?? []).filter { part in
            switch part.type {
            case "text", "action", "reminder", "page":
                return false
            default:
                return part.display != "page"
            }
        }
    }

    private var nonTextParts: [MessagePart] {
        Self.visibleNonTextParts(in: message)
    }

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            if showAvatar {
                NoriMark(size: 26, state: markState)
                    .matchedGeometryEffect(id: "active-nori-avatar", in: avatarNamespace)
                    .padding(.top, 2)
            } else {
                Color.clear
                    .frame(width: 26, height: 1)
                    .accessibilityHidden(true)
            }

            VStack(alignment: .leading, spacing: 8) {
                if !message.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    if MarkdownBlocksView.containsBlockSyntax(message.text) {
                        MarkdownBlocksView(markdown: message.text, bodyFont: NoriTypography.body, compact: true)
                    } else {
                        formattedMarkdownText(message.text)
                            .font(NoriTypography.body)
                            .lineSpacing(2.5)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }

                if !nonTextParts.isEmpty {
                    NoriMessagePartsView(parts: nonTextParts)
                }
            }
            .padding(.vertical, 4)
            .frame(maxWidth: .infinity, alignment: .leading)

            Spacer(minLength: 20)
        }
        .contentShape(Rectangle())
        .contextMenu {
            if hasCopyableText {
                Button {
                    onCopy(message.text)
                } label: {
                    Label("Copy", systemImage: "doc.on.doc")
                }
            }
        }
    }

    private func formattedMarkdownText(_ raw: String) -> Text {
        if let attributed = try? AttributedString(
            markdown: raw,
            options: AttributedString.MarkdownParsingOptions(
                interpretedSyntax: .inlineOnlyPreservingWhitespace
            )
        ) {
            return Text(attributed)
        }
        return Text(raw)
    }
}

private struct NoriMessagePartsView: View {
    let parts: [MessagePart]

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(parts) { part in
                partView(for: part)
            }
        }
    }

    @ViewBuilder
    private func partView(for part: MessagePart) -> some View {
        if part.type == "link",
           let rawURL = part.url,
           let url = URL(string: rawURL),
           let scheme = url.scheme?.lowercased(),
           scheme == "http" || scheme == "https" {
            Link(destination: url) {
                HStack(spacing: 6) {
                    Image(systemName: "link")
                        .font(.system(size: 12, weight: .semibold))
                    Text(part.name ?? rawURL)
                        .font(NoriTypography.metaSemibold)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                }
                .foregroundStyle(NoriColors.accent)
            }
        } else if ["attachment", "artifact", "image"].contains(part.type) {
            let label = part.name ?? part.title ?? "File"
            let isImage = part.type == "image" || (part.mediaType?.hasPrefix("image/") ?? false)
            HStack(spacing: 8) {
                Image(systemName: isImage ? "photo" : "doc.text")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(NoriColors.accent)
                    .frame(width: 28, height: 28)
                    .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                VStack(alignment: .leading, spacing: 1) {
                    Text(label)
                        .font(NoriTypography.metaSemibold)
                        .foregroundStyle(NoriColors.ink)
                        .lineLimit(1)
                    if let size = part.size, size > 0 {
                        Text(ByteCountFormatter.string(fromByteCount: Int64(size), countStyle: .file))
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink3)
                    }
                }
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 8)
            .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                    .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
            )
        } else if part.type == "card" {
            let title = part.title ?? part.name ?? part.fallback ?? "Item"
            let detail = part.text ?? (title == part.fallback ? nil : part.fallback)
            VStack(alignment: .leading, spacing: 3) {
                Text(title)
                    .font(NoriTypography.rowTitle)
                    .foregroundStyle(NoriColors.ink)
                    .lineLimit(2)
                if let detail, !detail.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Text(detail)
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink2)
                        .lineLimit(3)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                    .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
            )
        } else if part.type == "note", let noteText = part.text, !noteText.isEmpty {
            Text(noteText)
                .font(NoriTypography.caption)
                .foregroundStyle(NoriColors.ink3)
                .fixedSize(horizontal: false, vertical: true)
        } else if let fallback = part.fallback, !fallback.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            Text(fallback)
                .font(NoriTypography.body)
                .foregroundStyle(NoriColors.ink)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}

struct MessageReceiptView: View {
    let receipt: MessageReceipt

    var body: some View {
        HStack(spacing: 3) {
            if receipt == .saved {
                Image(systemName: "checkmark")
                    .font(.system(size: 10, weight: .semibold))
                Text("Saved")
            } else {
                Text("Sending…")
            }
        }
        .font(.system(size: 11, weight: .medium))
        .foregroundStyle(NoriColors.ink3)
    }
}

struct ChatErrorCard: View {
    let message: Message
    let isRetrying: Bool
    let onRetry: () -> Void
    let onOpenModels: () -> Void

    static func isModelKeyError(_ errorCode: String?) -> Bool {
        errorCode == "model_key_required" || errorCode == "missing_api_key"
    }

    private var showsOpenModels: Bool {
        Self.isModelKeyError(message.error?.code)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 9) {
                Image(systemName: "exclamationmark.circle.fill")
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundStyle(NoriColors.danger)
                    .padding(.top, 1)

                VStack(alignment: .leading, spacing: 3) {
                    Text("Couldn’t get a reply")
                        .font(.system(size: 13.5, weight: .semibold))
                        .foregroundStyle(NoriColors.ink)

                    Text(message.displayErrorMessage)
                        .font(.system(size: 13))
                        .lineSpacing(2)
                        .foregroundStyle(NoriColors.ink2)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }

            if message.isRetryableError || showsOpenModels {
                HStack(spacing: 8) {
                    if showsOpenModels {
                        Button("Open Models", action: onOpenModels)
                            .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                    }
                    if message.isRetryableError {
                        Button(action: onRetry) {
                            HStack(spacing: 6) {
                                if isRetrying {
                                    ProgressView()
                                        .controlSize(.mini)
                                        .tint(NoriColors.onAccent)
                                } else {
                                    Image(systemName: "arrow.clockwise")
                                        .font(.system(size: 11.5, weight: .semibold))
                                }
                                Text(isRetrying ? "Retrying…" : "Retry")
                            }
                        }
                        .buttonStyle(NoriButtonStyle(.primary, compact: true))
                        .disabled(isRetrying)
                        .accessibilityLabel(isRetrying ? "Retrying message" : "Retry message")
                    }

                    Spacer(minLength: 0)
                }
                .padding(.leading, 25)
            }
        }
        .padding(13)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            NoriColors.dangerBg,
            in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
        )
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
    }
}

struct PageBlockCard: View {
    let message: Message
    let part: MessagePart
    let onCopy: (String) -> Void
    let onOpen: () -> Void

    @State private var isExpanded: Bool = false
    @State private var copied: Bool = false

    init(
        message: Message,
        part: MessagePart,
        onCopy: @escaping (String) -> Void = { _ in },
        onOpen: @escaping () -> Void
    ) {
        self.message = message
        self.part = part
        self.onCopy = onCopy
        self.onOpen = onOpen
    }

    private var rawBody: String {
        part.text ?? part.fallback ?? (message.text.isEmpty ? "" : message.text)
    }

    private var presentation: (title: String, body: String) {
        MarkdownBlocksView.resolveDocumentPresentation(
            body: rawBody,
            explicitTitle: part.title
        )
    }

    private var isLongBody: Bool {
        let body = presentation.body
        return body.count > 520 || body.components(separatedBy: .newlines).count > 10
    }

    private var isCollapsed: Bool {
        isLongBody && !isExpanded
    }

    var body: some View {
        let resolved = presentation
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "doc.text")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(NoriColors.accent)
                    .frame(width: 28, height: 28)
                    .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 8, style: .continuous))

                Text(resolved.title)
                    .font(NoriTypography.metaSemibold)
                    .foregroundStyle(NoriColors.ink)
                    .lineLimit(1)

                Spacer(minLength: 6)

                if !rawBody.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Button {
                        onCopy(rawBody)
                        copied = true
                        Task {
                            try? await Task.sleep(for: .seconds(1.6))
                            copied = false
                        }
                    } label: {
                        HStack(spacing: 4) {
                            Image(systemName: copied ? "checkmark" : "doc.on.doc")
                                .font(.system(size: 11, weight: .semibold))
                            Text(copied ? "Copied" : "Copy")
                                .font(.system(size: 11.5, weight: .semibold))
                        }
                        .foregroundStyle(copied ? NoriColors.accent : NoriColors.ink2)
                        .padding(.horizontal, 9)
                        .frame(height: 26)
                        .background(NoriColors.elevated, in: Capsule())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(copied ? "Copied document" : "Copy document")
                }

                Button(action: onOpen) {
                    HStack(spacing: 4) {
                        Image(systemName: "doc.text")
                            .font(.system(size: 11, weight: .semibold))
                        Text("Open")
                            .font(.system(size: 11.5, weight: .semibold))
                    }
                    .foregroundStyle(NoriColors.accent)
                    .padding(.horizontal, 9)
                    .frame(height: 26)
                    .background(NoriColors.selected, in: Capsule())
                }
                .buttonStyle(.plain)
            }
            .padding(.bottom, 10)

            Rectangle()
                .fill(NoriColors.hairline)
                .frame(height: 1)
                .padding(.bottom, 10)

            if !resolved.body.isEmpty {
                ZStack(alignment: .bottom) {
                    MarkdownBlocksView(markdown: resolved.body, bodyFont: NoriTypography.body, compact: true)
                        .frame(maxHeight: isCollapsed ? 260 : nil, alignment: .top)
                        .clipped()

                    if isCollapsed {
                        LinearGradient(
                            colors: [NoriColors.surface.opacity(0), NoriColors.surface],
                            startPoint: .top,
                            endPoint: .bottom
                        )
                        .frame(height: 68)
                        .allowsHitTesting(false)

                        Button("Expand") {
                            withAnimation(.easeInOut(duration: 0.2)) {
                                isExpanded = true
                            }
                        }
                        .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                        .padding(.bottom, 2)
                    }
                }

                if isLongBody && isExpanded {
                    HStack {
                        Spacer()
                        Button("Collapse") {
                            withAnimation(.easeInOut(duration: 0.2)) {
                                isExpanded = false
                            }
                        }
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        Spacer()
                    }
                    .padding(.top, 8)
                }
            }

            if let metrics = part.metrics, !metrics.isEmpty {
                LazyVGrid(
                    columns: [
                        GridItem(.flexible(), spacing: 8),
                        GridItem(.flexible(), spacing: 8)
                    ],
                    spacing: 8
                ) {
                    ForEach(metrics) { metric in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(metric.label)
                                .font(.system(size: 11))
                                .foregroundStyle(NoriColors.ink3)
                                .lineLimit(1)
                            Text(metric.value)
                                .font(.system(size: 13.5, weight: .bold, design: .monospaced))
                                .foregroundStyle(NoriColors.ink)
                                .lineLimit(1)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 10)
                        .padding(.vertical, 8)
                        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                }
                .padding(.top, 10)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
        .shadow(color: NoriColors.cardShadow, radius: 8, x: 0, y: 2)
    }
}
