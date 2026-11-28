import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

public struct RoutinesView: View {
    @Bindable private var store: NoriStore
    @State private var selectedRoutineID: String?
    @State private var showNewRoutineSheet: Bool = false
    @State private var confirmingRemoveID: String?
    @State private var dismissedIDs: Set<String> = []

    public init(store: NoriStore) {
        self.store = store
    }

    private var activeAutomations: [Automation] {
        let nonCancelled = store.automations.filter {
            $0.status != .cancelled && $0.trigger.kind != "once" && !dismissedIDs.contains($0.id)
        }
        let enabled = nonCancelled.filter { $0.status == .enabled }
        let paused = nonCancelled.filter { $0.status != .enabled }
        return enabled + paused
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if let error = store.automationsRead.error {
                    NoriRetryNotice("Couldn’t load automations", detail: error) {
                        await store.refreshAutomations()
                    }
                }

                if activeAutomations.isEmpty && store.automationsRead == .pending {
                    NoriLoadingState("Loading automations…")
                } else if activeAutomations.isEmpty && store.automationsRead == .loaded {
                    NoriEmptyState(
                        title: "No automations yet",
                        message: "Ask Nori in Chat to set up recurring briefs, monitors, or scheduled checks."
                    ) {
                        Button {
                            showNewRoutineSheet = true
                        } label: {
                            Label("New automation", systemImage: "plus")
                        }
                        .buttonStyle(NoriButtonStyle(.primary, compact: true))
                    }
                } else if !activeAutomations.isEmpty {
                    NoriRows {
                        ForEach(Array(activeAutomations.enumerated()), id: \.element.id) { idx, automation in
                            automationRow(
                                automation,
                                showDivider: idx < activeAutomations.count - 1
                            )
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 10)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle("Automations")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .navigationDestination(item: $selectedRoutineID) { routineID in
            RoutineDetailView(store: store, automationID: routineID)
        }
        .sheet(isPresented: $showNewRoutineSheet) {
            RoutineEditorSheet(store: store) { created in
                selectedRoutineID = created.id
            }
            .presentationDetents([.medium, .large])
            .presentationDragIndicator(.visible)
            .presentationBackground(NoriColors.sheetBackground)
            .presentationCornerRadius(NoriRadius.sheet)
        }
        .refreshable {
            await store.refreshAutomations()
        }
    }

    private func automationRow(_ automation: Automation, showDivider: Bool) -> some View {
        let isEnabled = automation.status == .enabled && !automation.isSpentOneTime
        let isConfirming = confirmingRemoveID == automation.id

        return VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .center, spacing: 12) {
                Image(systemName: "clock")
                    .font(.system(size: 14, weight: .medium))
                    .foregroundStyle(isEnabled ? NoriColors.scheduled : NoriColors.ink3)
                    .frame(width: 20, height: 20)

                Text(automation.title)
                    .font(NoriTypography.rowTitle)
                    .foregroundStyle(isEnabled ? NoriColors.ink : NoriColors.ink2)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)

                if isConfirming {
                    HStack(spacing: 6) {
                        Button("Remove") {
                            confirmRemove(automation)
                        }
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(NoriColors.danger)
                        .padding(.horizontal, 10)
                        .frame(height: 28)
                        .background(NoriColors.dangerBg, in: Capsule())
                        .buttonStyle(.plain)

                        Button("Keep") {
                            withAnimation(.easeInOut(duration: 0.14)) {
                                confirmingRemoveID = nil
                            }
                        }
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(NoriColors.ink2)
                        .padding(.horizontal, 10)
                        .frame(height: 28)
                        .background(NoriColors.fill1, in: Capsule())
                        .buttonStyle(.plain)
                    }
                } else {
                    HStack(spacing: 8) {
                        Text(automation.trigger.compactCadenceLabel)
                            .font(.system(size: 12, weight: .medium))
                            .foregroundStyle(isEnabled ? NoriColors.scheduled : NoriColors.ink3)
                            .lineLimit(1)

                        Button {
                            withAnimation(.easeInOut(duration: 0.14)) {
                                confirmingRemoveID = automation.id
                            }
                        } label: {
                            Image(systemName: "xmark")
                                .font(.system(size: 11, weight: .semibold))
                                .foregroundStyle(NoriColors.ink3)
                                .frame(width: 26, height: 26)
                                .contentShape(Circle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Remove \(automation.title)")
                    }
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 13)
            .contentShape(Rectangle())
            .onTapGesture {
                if isConfirming {
                    withAnimation(.easeInOut(duration: 0.14)) {
                        confirmingRemoveID = nil
                    }
                } else {
                    selectedRoutineID = automation.id
                }
            }

            if showDivider {
                Rectangle()
                    .fill(NoriColors.hairline)
                    .frame(height: 1)
                    .padding(.leading, 46)
            }
        }
    }

    private func confirmRemove(_ automation: Automation) {
        #if os(iOS)
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
        #endif
        confirmingRemoveID = nil
        dismissedIDs.insert(automation.id)

        Task {
            let ok = await store.cancelAutomation(automationID: automation.id)
            if !ok {
                dismissedIDs.remove(automation.id)
            }
        }
    }
}
