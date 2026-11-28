import SwiftUI
import NoriCore
import NoriDesign

public struct PersonalizationSettingsView: View {
    public struct ResponseStyleOption: Identifiable, Hashable, Sendable {
        public let id: String
        public let detail: String

        public init(id: String, detail: String) {
            self.id = id
            self.detail = detail
        }
    }

    public static let responseStyleOptions: [ResponseStyleOption] = [
        ResponseStyleOption(id: "Concise & direct", detail: "Short, straight to the point, minimal filler"),
        ResponseStyleOption(id: "Warm & conversational", detail: "Friendly, natural tone with a bit more context"),
        ResponseStyleOption(id: "Bullets first", detail: "Structured takeaways up front for quick scanning"),
    ]

    public static let focusOptions: [String] = [
        "Engineering",
        "Daily planning",
        "Email & calendar",
        "Research",
        "Writing",
    ]

    private static let maxNameLength = 120
    private static let maxWorkingOnLength = 240
    private static let maxInstructionsLength = 4000

    @Bindable private var store: NoriStore
    @State private var name = ""
    @State private var replyStyle = ""
    @State private var customInstructions = ""
    @State private var focus: [String] = []
    @State private var workingOn = ""
    @State private var syncedRevision: Int?
    @State private var isSaving = false
    @State private var hasRevisionConflict = false
    @State private var errorMessage: String?

    public init(store: NoriStore) {
        self.store = store
    }

    private var savedPersonalization: PersonalizationPreferences {
        store.preferences.personalization ?? PersonalizationPreferences()
    }

    private var isTextDirty: Bool {
        name.trimmingCharacters(in: .whitespacesAndNewlines)
            != (savedPersonalization.name?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "")
        || customInstructions.trimmingCharacters(in: .whitespacesAndNewlines)
            != (savedPersonalization.customInstructions?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "")
        || workingOn.trimmingCharacters(in: .whitespacesAndNewlines)
            != (savedPersonalization.workingOn?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "")
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                if hasRevisionConflict {
                    NoriNotice(
                        title: "These changed on another device.",
                        detail: "Load the latest to edit them.",
                        tone: .warning
                    ) {
                        Button("Load latest") {
                            Task { await reloadLatest() }
                        }
                        .buttonStyle(NoriButtonStyle(.primary, compact: true))
                        .disabled(isSaving)
                    }
                    .padding(.bottom, 12)
                }

                if let errorMessage, !hasRevisionConflict {
                    NoriNotice(title: errorMessage, tone: .danger)
                        .padding(.bottom, 12)
                }

                NoriSectionHeader("Response style") {
                    if isTextDirty {
                        Button(isSaving ? "Saving…" : "Save") {
                            Task { await save() }
                        }
                        .buttonStyle(NoriButtonStyle(.primary, compact: true))
                        .disabled(isSaving)
                    }
                }
                NoriRows {
                    ForEach(Array(Self.responseStyleOptions.enumerated()), id: \.element.id) { index, option in
                        let isSelected = replyStyle == option.id
                        let isLast = index == Self.responseStyleOptions.count - 1
                        Button {
                            let previous = replyStyle
                            let next = isSelected ? "" : option.id
                            replyStyle = next
                            Task { await save(replyStyleOverride: next, onFailure: { replyStyle = previous }) }
                        } label: {
                            NoriRow(
                                title: option.id,
                                detail: option.detail,
                                showChevron: false,
                                showDivider: !isLast
                            ) {
                                if isSelected {
                                    Image(systemName: "checkmark")
                                        .font(.system(size: 14, weight: .semibold))
                                        .foregroundStyle(NoriColors.accent)
                                }
                            }
                        }
                        .buttonStyle(.plain)
                        .disabled(isSaving)
                    }
                }

                NoriSectionHeader("Custom instructions")
                NoriRows {
                    TextField(
                        "How should Nori respond? Add formatting preferences, tone guidelines, or rules to always follow.",
                        text: $customInstructions,
                        axis: .vertical
                    )
                    .lineLimit(3...8)
                    .font(NoriTypography.body)
                    .foregroundStyle(NoriColors.ink)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 12)
                    .onChange(of: customInstructions) { _, value in
                        if value.count > Self.maxInstructionsLength {
                            customInstructions = String(value.prefix(Self.maxInstructionsLength))
                        }
                    }
                }
                Text("Applied to every conversation alongside your response style.")
                    .font(NoriTypography.caption)
                    .foregroundStyle(NoriColors.ink3)
                    .padding(.horizontal, 4)
                    .padding(.top, 6)

                NoriSectionHeader("About you")
                NoriRows {
                    VStack(spacing: 0) {
                        HStack(spacing: 12) {
                            Text("Name")
                                .font(NoriTypography.rowTitle)
                                .foregroundStyle(NoriColors.ink)
                            TextField("What should Nori call you?", text: $name)
                                .font(NoriTypography.body)
                                .foregroundStyle(NoriColors.ink)
                                .multilineTextAlignment(.trailing)
                                #if os(iOS)
                                .textInputAutocapitalization(.words)
                                #endif
                                .onChange(of: name) { _, value in
                                    if value.count > Self.maxNameLength {
                                        name = String(value.prefix(Self.maxNameLength))
                                    }
                                }
                        }
                        .padding(.horizontal, 14)
                        .frame(minHeight: 50)

                        Divider().overlay(NoriColors.hairline)

                        HStack(spacing: 12) {
                            Text("Working on")
                                .font(NoriTypography.rowTitle)
                                .foregroundStyle(NoriColors.ink)
                            TextField("Current project or role", text: $workingOn)
                                .font(NoriTypography.body)
                                .foregroundStyle(NoriColors.ink)
                                .multilineTextAlignment(.trailing)
                                .onChange(of: workingOn) { _, value in
                                    if value.count > Self.maxWorkingOnLength {
                                        workingOn = String(value.prefix(Self.maxWorkingOnLength))
                                    }
                                }
                        }
                        .padding(.horizontal, 14)
                        .frame(minHeight: 50)

                        Divider().overlay(NoriColors.hairline)

                        VStack(alignment: .leading, spacing: 10) {
                            Text("Focus")
                                .font(NoriTypography.meta.weight(.semibold))
                                .foregroundStyle(NoriColors.ink2)

                            PersonalizationChipFlow(spacing: 8) {
                                ForEach(Self.focusOptions, id: \.self) { option in
                                    let selected = focus.contains(option)
                                    Button {
                                        let previous = focus
                                        var next = focus
                                        if let idx = next.firstIndex(of: option) {
                                            next.remove(at: idx)
                                        } else {
                                            next.append(option)
                                        }
                                        focus = next
                                        Task { await save(focusOverride: next, onFailure: { focus = previous }) }
                                    } label: {
                                        Text(option)
                                            .font(NoriTypography.meta.weight(selected ? .semibold : .medium))
                                            .foregroundStyle(selected ? NoriColors.chipSelectedFg : NoriColors.ink2)
                                            .padding(.horizontal, 12)
                                            .padding(.vertical, 6)
                                            .background(
                                                selected ? NoriColors.chipSelectedBg : NoriColors.fill1,
                                                in: Capsule()
                                            )
                                            .overlay(
                                                Capsule().strokeBorder(
                                                    selected ? NoriColors.chipSelectedBorder : NoriColors.cardBorder,
                                                    lineWidth: 1
                                                )
                                            )
                                    }
                                    .buttonStyle(.plain)
                                    .disabled(isSaving)
                                }
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 12)
                    }
                }

                if isTextDirty {
                    HStack {
                        Spacer()
                        Button(isSaving ? "Saving…" : "Save changes") {
                            Task { await save() }
                        }
                        .buttonStyle(NoriButtonStyle(.primary, compact: true))
                        .disabled(isSaving)
                    }
                    .padding(.top, 12)
                }

                NoriRows {
                    NavigationLink(value: MoreRoute.memory) {
                        NoriRow(
                            title: "Memory",
                            detail: "Manage details Nori learns automatically from your chats",
                            showChevron: true,
                            showDivider: false
                        )
                    }
                    .buttonStyle(.plain)
                }
                .padding(.top, 20)
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 40)
            .noriReadableColumn()
        }
        .refreshable {
            await reloadLatest()
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .navigationTitle("Personalization")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .task {
            syncFromStore(force: true)
            try? await store.refreshPreferencesThrowing()
            if !isTextDirty {
                syncFromStore(force: true)
            }
        }
        .onChange(of: store.preferences.revision) { _, newRevision in
            guard syncedRevision != newRevision else { return }
            if isTextDirty {
                hasRevisionConflict = true
            } else {
                syncFromStore(force: true)
            }
        }
    }

    private func reloadLatest() async {
        try? await store.refreshPreferencesThrowing()
        hasRevisionConflict = false
        errorMessage = nil
        syncFromStore(force: true)
    }

    private func syncFromStore(force: Bool) {
        if !force && syncedRevision == store.preferences.revision { return }
        let p = savedPersonalization
        name = p.name ?? ""
        replyStyle = p.replyStyle ?? ""
        customInstructions = p.customInstructions ?? ""
        focus = p.focus ?? []
        workingOn = p.workingOn ?? ""
        syncedRevision = store.preferences.revision
    }

    private func save(
        replyStyleOverride: String? = nil,
        focusOverride: [String]? = nil,
        onFailure: (() -> Void)? = nil
    ) async {
        guard !isSaving else { return }
        isSaving = true
        errorMessage = nil
        defer { isSaving = false }

        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedStyle = (replyStyleOverride ?? replyStyle).trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedInstructions = customInstructions.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedWorking = workingOn.trimmingCharacters(in: .whitespacesAndNewlines)
        let activeFocus = focusOverride ?? focus

        var updated = store.preferences
        updated.personalization = PersonalizationPreferences(
            name: trimmedName.isEmpty ? nil : trimmedName,
            replyStyle: trimmedStyle.isEmpty ? nil : trimmedStyle,
            customInstructions: trimmedInstructions.isEmpty ? nil : trimmedInstructions,
            focus: activeFocus.isEmpty ? nil : activeFocus,
            workingOn: trimmedWorking.isEmpty ? nil : trimmedWorking
        )

        do {
            try await store.savePreferences(updated)
            hasRevisionConflict = false
            syncFromStore(force: true)
        } catch let apiError as NoriAPIError where apiError.isRevisionConflict {
            onFailure?()
            hasRevisionConflict = true
        } catch {
            onFailure?()
            errorMessage = error.localizedDescription
        }
    }
}

private struct PersonalizationChipFlow: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let maxWidth = proposal.width ?? .infinity
        var rowWidth: CGFloat = 0
        var rowHeight: CGFloat = 0
        var totalHeight: CGFloat = 0
        var maxRowWidth: CGFloat = 0

        for view in subviews {
            let size = view.sizeThatFits(.unspecified)
            if rowWidth > 0 && rowWidth + spacing + size.width > maxWidth {
                totalHeight += rowHeight + spacing
                maxRowWidth = max(maxRowWidth, rowWidth)
                rowWidth = size.width
                rowHeight = size.height
            } else {
                rowWidth = rowWidth > 0 ? rowWidth + spacing + size.width : size.width
                rowHeight = max(rowHeight, size.height)
            }
        }
        totalHeight += rowHeight
        maxRowWidth = max(maxRowWidth, rowWidth)
        return CGSize(width: proposal.width ?? maxRowWidth, height: totalHeight)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX
        var y = bounds.minY
        var rowHeight: CGFloat = 0

        for view in subviews {
            let size = view.sizeThatFits(.unspecified)
            if x > bounds.minX && x + size.width > bounds.maxX {
                x = bounds.minX
                y += rowHeight + spacing
                rowHeight = 0
            }
            view.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }
    }
}
