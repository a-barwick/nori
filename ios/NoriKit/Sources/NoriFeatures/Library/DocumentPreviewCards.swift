import SwiftUI
import NoriCore
import NoriDesign

struct DocumentTextCard: View {
    let text: String
    let isLoadingFull: Bool
    let isMonospace: Bool

    var body: some View {
        NoriCard(padding: 16) {
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
                } else {
                    MarkdownBlocksView(markdown: text)
                        .textSelection(.enabled)
                }
            }
        }
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
                            Label("Zoom", systemImage: "arrow.up.left.and.arrow.down.right")
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
                        Label("Open PDF", systemImage: "eye")
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
                        Label("Quick Look", systemImage: "eye")
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                }
                #endif
            }
        }
    }
}
