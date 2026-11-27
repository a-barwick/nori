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
                    .foregroundStyle(Color.white)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 5)
                    .background(Color.white.opacity(0.16), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
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

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            if showAvatar {
                NoriMark(size: 28, state: markState)
                    .matchedGeometryEffect(id: "active-nori-avatar", in: avatarNamespace)
                    .padding(.top, 2)
            } else {
                Color.clear
                    .frame(width: 28, height: 1)
                    .accessibilityHidden(true)
            }

            formattedMarkdownText(message.text)
                .font(NoriTypography.body)
                .lineSpacing(2.5)
                .foregroundStyle(NoriColors.ink)
                .tint(NoriColors.accent)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.vertical, 4)

            Spacer(minLength: 28)
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

            if message.isRetryableError || message.error?.code == "model_key_required" {
                HStack {
                    if message.error?.code == "model_key_required" {
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
            NoriColors.dangerBg.opacity(0.65),
            in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
        )
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(NoriColors.danger.opacity(0.24), lineWidth: 1)
        )
    }
}

struct PageBlockCard: View {
    let message: Message
    let part: MessagePart
    let onOpen: () -> Void

    private var title: String {
        part.title ?? (message.text.isEmpty ? "Saved Document" : message.text)
    }

    private var summary: String? {
        part.text ?? part.fallback
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                NoriMark(size: 16, state: .idle)
                Text("Nori")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)
                if !message.createdAt.isEmpty {
                    Text("· \(NoriDateFormatting.shortDateTime(message.createdAt))")
                        .font(.system(size: 12))
                        .foregroundStyle(NoriColors.ink2)
                }
                Spacer(minLength: 8)
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
                    .background(NoriColors.accentSubtle, in: Capsule())
                }
                .buttonStyle(.plain)
            }
            .padding(.bottom, 10)

            Rectangle()
                .fill(NoriColors.cardBorder)
                .frame(height: 0.5)
                .padding(.bottom, 10)

            Text(title)
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(NoriColors.ink)
                .padding(.bottom, 6)

            if let summary, !summary.isEmpty {
                Text(summary)
                    .font(.system(size: 13.5))
                    .lineSpacing(2.5)
                    .foregroundStyle(NoriColors.ink2)
                    .fixedSize(horizontal: false, vertical: true)
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
