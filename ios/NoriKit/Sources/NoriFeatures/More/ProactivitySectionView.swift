import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

public enum ProactivityTimeFormatting {
    public static func formatClock(_ hhmm: String) -> String {
        let parts = hhmm.split(separator: ":")
        guard parts.count == 2,
              let hour24 = Int(parts[0]),
              let minute = Int(parts[1]),
              (0...23).contains(hour24),
              (0...59).contains(minute) else {
            return hhmm
        }
        let suffix = hour24 >= 12 ? "PM" : "AM"
        let hour12 = hour24 % 12 == 0 ? 12 : hour24 % 12
        return String(format: "%d:%02d %@", hour12, minute, suffix)
    }

    public static func quietHoursLabel(_ quietHours: QuietHours?) -> String {
        guard let quietHours else {
            return "\(formatClock("21:00")) – \(formatClock("07:30"))"
        }
        guard quietHours.isEnabled else { return "Off" }
        return "\(formatClock(quietHours.start)) – \(formatClock(quietHours.end))"
    }

    public static func morningBriefLabel(_ proactivity: ProactivityPreferences?) -> String {
        guard proactivity?.morningBriefEnabled ?? true else { return "Off" }
        let raw = proactivity?.morningBriefTime?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return formatClock(raw.isEmpty ? "08:00" : raw)
    }

    public static func dateFromHHMM(_ hhmm: String, fallbackHour: Int, fallbackMinute: Int) -> Date {
        let parts = hhmm.split(separator: ":")
        let hour = parts.count == 2 ? (Int(parts[0]) ?? fallbackHour) : fallbackHour
        let minute = parts.count == 2 ? (Int(parts[1]) ?? fallbackMinute) : fallbackMinute
        var comps = Calendar.current.dateComponents([.year, .month, .day], from: Date())
        comps.hour = hour
        comps.minute = minute
        return Calendar.current.date(from: comps) ?? Date()
    }

    public static func hhmmFromDate(_ date: Date) -> String {
        let comps = Calendar.current.dateComponents([.hour, .minute], from: date)
        return String(format: "%02d:%02d", comps.hour ?? 8, comps.minute ?? 0)
    }
}

public struct NotificationsSettingsView: View {
    @Bindable private var store: NoriStore

    @State private var showQuietHoursSheet = false
    @State private var showMorningBriefSheet = false

    public init(store: NoriStore) {
        self.store = store
    }

    private var proactivity: ProactivityPreferences {
        store.preferences.proactivity ?? ProactivityPreferences()
    }

    private var isMorningBriefScheduled: Bool {
        proactivity.morningBriefEnabled ?? true
    }

    private var pushChatsEnabled: Bool {
        proactivity.isPushChatsEnabled
    }

    private var pushUrgentEnabled: Bool {
        proactivity.isPushUrgentAndApprovalsEnabled
    }

    private var pushBriefEnabled: Bool {
        isMorningBriefScheduled && proactivity.isPushMorningBriefEnabled
    }

    private var allPushOff: Bool {
        !pushChatsEnabled && !pushUrgentEnabled && !pushBriefEnabled
    }

    private var quietHoursValue: String {
        ProactivityTimeFormatting.quietHoursLabel(store.preferences.quietHours)
    }

    private var morningBriefValue: String {
        ProactivityTimeFormatting.morningBriefLabel(store.preferences.proactivity)
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Push notifications")
                    NoriRows {
                        toggleRow(
                            title: "Chats",
                            isOn: Binding(
                                get: { pushChatsEnabled },
                                set: { savePushFlags(chats: $0, urgent: pushUrgentEnabled, brief: proactivity.isPushMorningBriefEnabled) }
                            ),
                            disabled: false,
                            showDivider: true
                        )

                        toggleRow(
                            title: "Urgent & approvals",
                            isOn: Binding(
                                get: { pushUrgentEnabled },
                                set: { savePushFlags(chats: pushChatsEnabled, urgent: $0, brief: proactivity.isPushMorningBriefEnabled) }
                            ),
                            disabled: false,
                            showDivider: true
                        )

                        toggleRow(
                            title: "Morning brief",
                            isOn: Binding(
                                get: { pushBriefEnabled },
                                set: { savePushFlags(chats: pushChatsEnabled, urgent: pushUrgentEnabled, brief: $0) }
                            ),
                            disabled: !isMorningBriefScheduled,
                            showDivider: false
                        )
                    }

                    if allPushOff {
                        Text("Push notifications are off. Updates still appear in Chat.")
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink3)
                            .padding(.top, 8)
                            .padding(.horizontal, 4)
                    }
                }

                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Schedule")
                    NoriRows {
                        Button {
                            showQuietHoursSheet = true
                        } label: {
                            NoriRow(title: "Quiet hours", showChevron: true, showDivider: true) {
                                Text(quietHoursValue)
                                    .font(NoriTypography.meta)
                                    .monospacedDigit()
                                    .foregroundStyle(NoriColors.ink2)
                            }
                        }
                        .buttonStyle(.plain)

                        Button {
                            showMorningBriefSheet = true
                        } label: {
                            NoriRow(title: "Morning brief", showChevron: true, showDivider: false) {
                                Text(morningBriefValue)
                                    .font(NoriTypography.meta)
                                    .monospacedDigit()
                                    .foregroundStyle(NoriColors.ink2)
                            }
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .task {
            if !store.hasLoadedPreferences {
                try? await store.refreshPreferencesThrowing()
            }
        }
        .refreshable {
            try? await store.refreshPreferencesThrowing()
        }
        .background(NoriColors.canvas)
        .navigationTitle("Notifications")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .sheet(isPresented: $showQuietHoursSheet) {
            QuietHoursEditorSheet(store: store)
                .presentationDetents([.medium])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
        .sheet(isPresented: $showMorningBriefSheet) {
            MorningBriefEditorSheet(store: store)
                .presentationDetents([.medium])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
    }

    private func toggleRow(title: String, isOn: Binding<Bool>, disabled: Bool, showDivider: Bool) -> some View {
        VStack(spacing: 0) {
            HStack(spacing: 12) {
                Text(title)
                    .font(NoriTypography.rowTitle)
                    .foregroundStyle(disabled ? NoriColors.disabled : NoriColors.ink)
                    .lineLimit(1)

                Spacer(minLength: 8)

                Toggle("", isOn: isOn)
                    .labelsHidden()
                    .tint(NoriColors.brandPrimary)
                    .disabled(disabled)
            }
            .padding(.horizontal, 15)
            .padding(.vertical, 10)
            .opacity(disabled ? 0.55 : 1)

            if showDivider {
                Rectangle()
                    .fill(NoriColors.hairline)
                    .frame(height: 1)
                    .padding(.leading, 15)
            }
        }
    }

    private func savePushFlags(chats: Bool, urgent: Bool, brief: Bool) {
        #if os(iOS)
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
        #endif
        var updated = store.preferences
        var nextProactivity = updated.proactivity ?? ProactivityPreferences()
        nextProactivity.pushChats = chats
        nextProactivity.pushUrgentAndApprovals = urgent
        nextProactivity.pushMorningBrief = brief
        nextProactivity.pushPolicy = ProactivityPreferences.derivedPushPolicy(chats: chats, urgent: urgent, brief: brief)
        updated.proactivity = nextProactivity
        store.preferences = updated
        Task {
            await store.updatePreferences(updated)
        }
    }
}

private struct QuietHoursEditorSheet: View {
    @Bindable var store: NoriStore
    @Environment(\.dismiss) private var dismiss

    @State private var enabled: Bool
    @State private var startDate: Date
    @State private var endDate: Date
    @State private var isSaving = false

    init(store: NoriStore) {
        self.store = store
        let qh = store.preferences.quietHours
        self._enabled = State(initialValue: qh?.isEnabled ?? true)
        self._startDate = State(initialValue: ProactivityTimeFormatting.dateFromHHMM(qh?.start ?? "21:00", fallbackHour: 21, fallbackMinute: 0))
        self._endDate = State(initialValue: ProactivityTimeFormatting.dateFromHHMM(qh?.end ?? "07:30", fallbackHour: 7, fallbackMinute: 30))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Text("Quiet hours")
                    .font(.system(size: 17, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)
                Spacer()
                Button {
                    dismiss()
                } label: {
                    Image(systemName: "xmark")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(NoriColors.ink2)
                        .frame(width: 28, height: 28)
                        .background(NoriColors.elevated, in: Circle())
                }
                .buttonStyle(.plain)
            }

            NoriRows {
                HStack {
                    Text("Enabled")
                        .font(NoriTypography.rowTitle)
                        .foregroundStyle(NoriColors.ink)
                    Spacer()
                    Toggle("", isOn: $enabled)
                        .labelsHidden()
                        .tint(NoriColors.brandPrimary)
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 10)

                if enabled {
                    Rectangle().fill(NoriColors.hairline).frame(height: 1).padding(.leading, 14)
                    DatePicker("Starts", selection: $startDate, displayedComponents: .hourAndMinute)
                        .font(NoriTypography.rowTitle)
                        .foregroundStyle(NoriColors.ink)
                        .tint(NoriColors.accent)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 8)

                    Rectangle().fill(NoriColors.hairline).frame(height: 1).padding(.leading, 14)
                    DatePicker("Ends", selection: $endDate, displayedComponents: .hourAndMinute)
                        .font(NoriTypography.rowTitle)
                        .foregroundStyle(NoriColors.ink)
                        .tint(NoriColors.accent)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 8)
                }
            }

            HStack {
                Spacer()
                Button(isSaving ? "Saving…" : "Save") {
                    save()
                }
                .buttonStyle(NoriButtonStyle(.primary))
                .disabled(isSaving)
            }
        }
        .padding(20)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background(NoriColors.sheetBackground)
    }

    private func save() {
        isSaving = true
        var updated = store.preferences
        updated.quietHours = QuietHours(
            enabled: enabled,
            start: ProactivityTimeFormatting.hhmmFromDate(startDate),
            end: ProactivityTimeFormatting.hhmmFromDate(endDate)
        )
        Task {
            await store.updatePreferences(updated)
            isSaving = false
            dismiss()
        }
    }
}

private struct MorningBriefEditorSheet: View {
    @Bindable var store: NoriStore
    @Environment(\.dismiss) private var dismiss

    @State private var enabled: Bool
    @State private var briefDate: Date
    @State private var isSaving = false

    init(store: NoriStore) {
        self.store = store
        let proactivity = store.preferences.proactivity
        self._enabled = State(initialValue: proactivity?.morningBriefEnabled ?? true)
        self._briefDate = State(initialValue: ProactivityTimeFormatting.dateFromHHMM(proactivity?.morningBriefTime ?? "08:00", fallbackHour: 8, fallbackMinute: 0))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Text("Morning brief")
                    .font(.system(size: 17, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)
                Spacer()
                Button {
                    dismiss()
                } label: {
                    Image(systemName: "xmark")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(NoriColors.ink2)
                        .frame(width: 28, height: 28)
                        .background(NoriColors.elevated, in: Circle())
                }
                .buttonStyle(.plain)
            }

            NoriRows {
                HStack {
                    Text("Enabled")
                        .font(NoriTypography.rowTitle)
                        .foregroundStyle(NoriColors.ink)
                    Spacer()
                    Toggle("", isOn: $enabled)
                        .labelsHidden()
                        .tint(NoriColors.brandPrimary)
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 10)

                if enabled {
                    Rectangle().fill(NoriColors.hairline).frame(height: 1).padding(.leading, 14)
                    DatePicker("Time", selection: $briefDate, displayedComponents: .hourAndMinute)
                        .font(NoriTypography.rowTitle)
                        .foregroundStyle(NoriColors.ink)
                        .tint(NoriColors.accent)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 8)
                }
            }

            HStack {
                Spacer()
                Button(isSaving ? "Saving…" : "Save") {
                    save()
                }
                .buttonStyle(NoriButtonStyle(.primary))
                .disabled(isSaving)
            }
        }
        .padding(20)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background(NoriColors.sheetBackground)
    }

    private func save() {
        isSaving = true
        var updated = store.preferences
        var proactivity = updated.proactivity ?? ProactivityPreferences()
        proactivity.morningBriefEnabled = enabled
        proactivity.morningBriefTime = ProactivityTimeFormatting.hhmmFromDate(briefDate)
        updated.proactivity = proactivity
        Task {
            await store.updatePreferences(updated)
            isSaving = false
            dismiss()
        }
    }
}
