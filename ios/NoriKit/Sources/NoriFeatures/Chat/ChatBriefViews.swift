import SwiftUI
import NoriCore
import NoriDesign

public enum BriefItemStatus: Sendable, Equatable {
    case pending
    case sent
    case skipped

    public static func resolve(interactionID: String, interactions: [Interaction]) -> BriefItemStatus {
        resolve(interactionID: interactionID, interactions: interactions, visited: [])
    }

    private static func resolve(
        interactionID: String,
        interactions: [Interaction],
        visited: Set<String>
    ) -> BriefItemStatus {
        guard !visited.contains(interactionID),
              let interaction = interactions.first(where: { $0.id == interactionID }) else {
            return .pending
        }
        switch interaction.status {
        case .pending:
            return .pending
        case .expired:
            return .skipped
        case .superseded:
            if let replacementID = interaction.supersededBy, !replacementID.isEmpty {
                if interactions.contains(where: { $0.id == replacementID }) {
                    var nextVisited = visited
                    nextVisited.insert(interactionID)
                    return resolve(interactionID: replacementID, interactions: interactions, visited: nextVisited)
                }
                return .sent
            }
            return .skipped
        case .resolved:
            switch interaction.resolution?.kind {
            case .decline, .cancel, .expired:
                return .skipped
            default:
                return .sent
            }
        }
    }
}

struct BriefCardView: View {
    let part: MessagePart
    let interactions: [Interaction]
    let onReview: (String) -> Void

    private var greeting: String {
        let trimmed = part.greeting?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? "Good morning" : trimmed
    }

    private var meta: String? {
        let trimmed = part.meta?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? nil : trimmed
    }

    private var items: [BriefItem] {
        part.items ?? []
    }

    private var emptyItemsBody: String? {
        let raw = (part.text ?? part.fallback)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return raw.isEmpty ? nil : raw
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(greeting)
                    .font(.system(size: 17, weight: .semibold))
                    .tracking(-0.2)
                    .foregroundStyle(NoriColors.ink)
                    .lineLimit(1)

                Spacer(minLength: 8)

                if let meta {
                    Text(meta)
                        .font(.system(size: 12.5, weight: .medium))
                        .foregroundStyle(NoriColors.ink2)
                        .lineLimit(1)
                }
            }
            .padding(.horizontal, 18)
            .padding(.top, 16)
            .padding(.bottom, 14)

            if !items.isEmpty {
                VStack(spacing: 0) {
                    Rectangle()
                        .fill(NoriColors.hairline)
                        .frame(height: 1)

                    ForEach(Array(items.enumerated()), id: \.element.id) { idx, item in
                        briefRow(item, showDivider: idx < items.count - 1)
                    }
                }
                .background(NoriColors.surface)
            } else if let bodyText = emptyItemsBody {
                Text(bodyText)
                    .font(.system(size: 15))
                    .lineSpacing(3)
                    .foregroundStyle(NoriColors.ink2)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 18)
                    .padding(.bottom, 16)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            NoriColors.surfaceTinted,
            in: RoundedRectangle(cornerRadius: 20, style: .continuous)
        )
        .overlay(
            RoundedRectangle(cornerRadius: 20, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
        .clipShape(RoundedRectangle(cornerRadius: 20, style: .continuous))
    }

    private func briefRow(_ item: BriefItem, showDivider: Bool) -> some View {
        VStack(spacing: 0) {
            HStack(alignment: .center, spacing: 12) {
                Text(item.title)
                    .font(.system(size: 14.5, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)

                trailingView(for: item)
            }
            .frame(minHeight: 48)
            .padding(.horizontal, 16)
            .padding(.vertical, 10)

            if showDivider {
                Rectangle()
                    .fill(NoriColors.hairline)
                    .frame(height: 1)
            }
        }
    }

    @ViewBuilder
    private func trailingView(for item: BriefItem) -> some View {
        if let interactionID = item.interactionID, !interactionID.isEmpty {
            let status = BriefItemStatus.resolve(interactionID: interactionID, interactions: interactions)
            switch status {
            case .pending:
                Button {
                    onReview(interactionID)
                } label: {
                    Text(item.actionLabel?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false ? item.actionLabel! : "Review")
                        .font(.system(size: 12.5, weight: .semibold))
                        .foregroundStyle(NoriColors.accent)
                        .padding(.horizontal, 12)
                        .padding(.vertical, 5)
                        .background(NoriColors.accentSubtle, in: Capsule())
                }
                .buttonStyle(.plain)
            case .sent:
                NoriPill("Sent", tone: .ok)
            case .skipped:
                NoriPill("Skipped", tone: .neutral)
            }
        } else if let trailing = item.trailing?.trimmingCharacters(in: .whitespacesAndNewlines), !trailing.isEmpty {
            Text(trailing)
                .font(.system(size: 12.5, weight: .medium))
                .foregroundStyle(NoriColors.ink2)
                .lineLimit(1)
        }
    }
}
