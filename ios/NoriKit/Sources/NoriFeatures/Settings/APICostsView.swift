import SwiftUI
import NoriCore
import NoriDesign

private struct APICostRequest: Hashable {
    let days: Int
    let account: String?
    let resetAt: String?
    let resetting: Bool
}

private struct APICostOwner: Hashable {
    let account: String?
    let resetAt: String?
}

public struct APICostsView: View {
    @Bindable private var store: NoriStore
    @State private var days = 30
    @State private var response: APICostResponse?
    @State private var selectedModel: String?
    @State private var selectedBucket = 0
    @State private var loading = true
    @State private var loadError: String?
    @State private var requestID = UUID()
    @State private var owner: APICostOwner?
    @State private var showingCostInfo = false

    public init(store: NoriStore) {
        self.store = store
    }

    private var request: APICostRequest {
        APICostRequest(
            days: days,
            account: store.authState.email,
            resetAt: store.preferences.dataResetAt,
            resetting: store.isResettingData
        )
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                NoriSegmentedTabs(
                    options: [
                        NoriSegmentOption(value: 1, label: "1 day"),
                        NoriSegmentOption(value: 7, label: "7 days"),
                        NoriSegmentOption(value: 30, label: "30 days")
                    ],
                    selection: $days
                )

                if loading || response?.days != days && loadError == nil {
                    NoriCard {
                        HStack {
                            Spacer()
                            ProgressView().tint(NoriColors.accent)
                            Spacer()
                        }
                        .frame(minHeight: 100)
                    }
                    .accessibilityLabel("Loading API costs")
                } else if let loadError {
                    NoriNotice(title: "Could not load API costs", detail: loadError, tone: .danger) {
                        Button("Try again") {
                            Task { await load() }
                        }
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                    }
                } else if let response {
                    costContent(response)
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle("API Costs")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .task(id: request) {
            if store.isResettingData || !store.authState.isSignedIn {
                requestID = UUID()
                response = nil
                loading = true
                selectedModel = nil
                owner = nil
            } else {
                await load()
            }
        }
        .task(id: store.authState.email) {
            if store.authState.isSignedIn && store.modelSettings == nil && store.capabilities?.modelProfiles == nil {
                try? await store.refreshModelSettings()
            }
        }
        .refreshable { await load() }
        .sheet(isPresented: $showingCostInfo) {
            NavigationStack {
                ScrollView {
                    VStack(alignment: .leading, spacing: 16) {
                        Text("Amounts are in US dollars. They combine costs reported by model providers and costs Nori calculates from recorded usage.")
                        Text("Calls with unavailable prices are counted separately. Local model calls have no API charge.")
                        Text("Dates follow your Nori time zone. Today includes calls recorded so far.")
                        Text("For the amount billed to your account, check your provider’s invoice.")
                    }
                    .font(NoriTypography.body)
                    .foregroundStyle(NoriColors.ink2)
                    .padding(20)
                }
                .background(NoriColors.sheetBackground)
                .navigationTitle("About recorded cost")
                #if os(iOS)
                .navigationBarTitleDisplayMode(.inline)
                #endif
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { showingCostInfo = false }
                    }
                }
            }
            .presentationDetents([.medium, .large])
            .presentationDragIndicator(.visible)
            .presentationBackground(NoriColors.sheetBackground)
            .presentationCornerRadius(NoriRadius.sheet)
        }
    }

    @ViewBuilder
    private func costContent(_ response: APICostResponse) -> some View {
        let amount = response.amount(for: selectedModel)
        NoriCard {
            VStack(alignment: .leading, spacing: 0) {
                HStack(alignment: .top, spacing: 8) {
                    VStack(alignment: .leading, spacing: 4) {
                        HStack(spacing: 6) {
                            Text("Recorded cost")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink2)
                            Button {
                                showingCostInfo = true
                            } label: {
                                Image(systemName: "info.circle")
                                    .font(.system(size: 14))
                                    .foregroundStyle(NoriColors.ink2)
                                    .frame(width: 36, height: 36)
                                    .contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel("About recorded cost")
                        }
                        .frame(height: 24, alignment: .leading)

                        Text(amount.totalCalls > 0 && !amount.hasKnownCost ? "—" : costString(amount.costUSD))
                            .font(.system(size: 32, weight: .bold))
                            .monospacedDigit()
                            .foregroundStyle(NoriColors.ink)
                    }

                    Spacer(minLength: 8)

                    VStack(alignment: .trailing, spacing: 8) {
                        Text(dateRange(response))
                            .font(NoriTypography.meta)
                            .foregroundStyle(NoriColors.ink2)
                            .multilineTextAlignment(.trailing)
                        if let selectedModel {
                            Button {
                                self.selectedModel = nil
                            } label: {
                                HStack(spacing: 5) {
                                    Text(modelName(selectedModel))
                                        .lineLimit(2)
                                    Image(systemName: "xmark")
                                        .font(.system(size: 10, weight: .bold))
                                }
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.accent)
                                .padding(.horizontal, 10)
                                .padding(.vertical, 8)
                                .background(NoriColors.selected, in: Capsule())
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel("Clear model filter, \(modelName(selectedModel))")
                        }
                    }
                }

                if amount.totalCalls == 0 {
                    VStack(spacing: 8) {
                        Image(systemName: "chart.bar")
                            .font(.system(size: 23))
                            .foregroundStyle(NoriColors.accent)
                        Text("No model calls in this range")
                            .font(.system(size: 15, weight: .semibold))
                            .foregroundStyle(NoriColors.ink)
                    }
                    .frame(maxWidth: .infinity, minHeight: 145)
                } else if !response.buckets.isEmpty {
                    chart(response)
                        .padding(.top, 18)
                }
            }
        }

        if amount.unpricedCalls > 0 {
            NoriNotice(
                title: unknownMessage(amount),
                tone: .warning,
                systemImage: "info.circle"
            )
        }

        if amount.pendingCalls > 0 {
            NoriNotice(
                title: "\(callCount(amount.pendingCalls)) in progress.",
                tone: .neutral,
                systemImage: "clock"
            )
        }

        if !response.models.isEmpty {
            VStack(alignment: .leading, spacing: 0) {
                NoriSectionHeader("By model")
                NoriRows {
                    ForEach(Array(response.models.enumerated()), id: \.element.id) { index, model in
                        Button {
                            selectedModel = selectedModel == model.model ? nil : model.model
                        } label: {
                            NoriRow(
                                title: modelName(model.model),
                                detail: modelDetail(model.amount),
                                showDivider: index < response.models.count - 1,
                                wrapTitle: true
                            ) {
                                HStack(spacing: 6) {
                                    Text(modelCost(model.amount))
                                        .font(.system(size: model.amount.hasKnownCost ? 14.5 : 12.5, weight: .semibold))
                                        .monospacedDigit()
                                        .foregroundStyle(model.amount.hasKnownCost ? NoriColors.ink : NoriColors.ink2)
                                    if selectedModel == model.model {
                                        Image(systemName: "checkmark")
                                            .font(.system(size: 12, weight: .semibold))
                                            .foregroundStyle(NoriColors.accent)
                                    }
                                }
                            }
                        }
                        .buttonStyle(.plain)
                        .accessibilityAddTraits(selectedModel == model.model ? .isSelected : [])
                    }
                }
            }
        }
    }

    private func chart(_ response: APICostResponse) -> some View {
        let buckets = response.buckets
        let index = min(selectedBucket, buckets.count - 1)
        let peak = buckets.map { $0.amount(for: selectedModel).costUSD }.max() ?? 0
        let step = chartStep(peak)
        let limit = step * 3
        let ticks = peak > 0 ? [0, 1, 2, 3] : [0]
        let selected = buckets[index]
        let selectedAmount = selected.amount(for: selectedModel)
        let labels = bucketLabels(response, short: false)
        let shortLabels = response.bucketSize == "hour" ? labels : bucketLabels(response, short: true)

        return VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .top, spacing: 6) {
                VStack(alignment: .trailing, spacing: 0) {
                    ForEach(ticks.reversed(), id: \.self) { tick in
                        Text(axisCost(step * Double(tick)))
                            .font(.system(size: 10))
                            .foregroundStyle(NoriColors.ink3)
                            .monospacedDigit()
                            .lineLimit(1)
                            .fixedSize(horizontal: true, vertical: false)
                        if tick > 0 {
                            Spacer(minLength: 0)
                        }
                    }
                }
                .frame(height: 140, alignment: .bottomTrailing)

                GeometryReader { geometry in
                    ZStack(alignment: .bottom) {
                        VStack(spacing: 0) {
                            ForEach(ticks.reversed(), id: \.self) { tick in
                                Rectangle()
                                    .fill(NoriColors.hairline)
                                    .frame(height: 1)
                                if tick > 0 { Spacer(minLength: 0) }
                            }
                        }
                        .frame(maxHeight: .infinity, alignment: .bottom)
                        HStack(alignment: .bottom, spacing: 2) {
                            ForEach(Array(buckets.enumerated()), id: \.element.id) { offset, bucket in
                                let value = bucket.amount(for: selectedModel)
                                Button {
                                    selectedBucket = offset
                                } label: {
                                    ZStack(alignment: .bottom) {
                                        RoundedRectangle(cornerRadius: 4)
                                            .fill(offset == index ? NoriColors.accent.opacity(0.08) : .clear)
                                        if value.costUSD > 0 {
                                            RoundedRectangle(cornerRadius: 3)
                                                .fill(offset == index ? NoriColors.accent : NoriColors.accent.opacity(0.34))
                                                .frame(height: max(2, geometry.size.height * min(1, value.costUSD / limit)))
                                                .padding(.horizontal, 1)
                                        }
                                        if value.unpricedCalls > 0 {
                                            Circle()
                                                .fill(NoriColors.warning)
                                                .frame(width: 5, height: 5)
                                                .offset(y: 9)
                                        }
                                    }
                                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                                    .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                                .accessibilityLabel(bucketAccessibility(label: labels[offset], amount: value))
                                .accessibilityAddTraits(offset == index ? .isSelected : [])
                            }
                        }
                    }
                }
                .frame(height: 140)
            }

            HStack(spacing: 6) {
                Text(axisCost(limit))
                    .font(.system(size: 10))
                    .monospacedDigit()
                    .lineLimit(1)
                    .fixedSize(horizontal: true, vertical: false)
                    .hidden()
                HStack {
                    ForEach(axisIndices(buckets.count), id: \.self) { offset in
                        if offset > 0 { Spacer(minLength: 0) }
                        Text(shortLabels[offset])
                            .font(.system(size: 10))
                            .foregroundStyle(NoriColors.ink3)
                            .lineLimit(1)
                    }
                }
            }
            .padding(.top, 13)
            .accessibilityHidden(true)

            Rectangle()
                .fill(NoriColors.hairline)
                .frame(height: 1)
                .padding(.top, 14)

            HStack(spacing: 6) {
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 7) {
                        Text(labels[index])
                            .foregroundStyle(NoriColors.ink2)
                        Text(periodCost(selectedAmount))
                            .fontWeight(.semibold)
                            .foregroundStyle(NoriColors.ink)
                            .monospacedDigit()
                    }
                    .font(NoriTypography.meta)
                    if selectedAmount.unpricedCalls > 0 {
                        Text("\(callCount(selectedAmount.unpricedCalls)) without cost")
                            .font(.system(size: 12))
                            .foregroundStyle(NoriColors.warning)
                    }
                    if selectedAmount.pendingCalls > 0 && periodCost(selectedAmount) != "In progress" {
                        Text("\(callCount(selectedAmount.pendingCalls)) in progress")
                            .font(.system(size: 12))
                            .foregroundStyle(NoriColors.ink2)
                    }
                }
                .accessibilityElement(children: .combine)

                Spacer(minLength: 0)

                Button { selectedBucket = max(0, index - 1) } label: {
                    Image(systemName: "chevron.left").frame(width: 44, height: 44)
                }
                .disabled(index == 0)
                .accessibilityLabel("Previous period")

                Button { selectedBucket = min(buckets.count - 1, index + 1) } label: {
                    Image(systemName: "chevron.right").frame(width: 44, height: 44)
                }
                .disabled(index == buckets.count - 1)
                .accessibilityLabel("Next period")
            }
            .font(.system(size: 13, weight: .semibold))
            .foregroundStyle(NoriColors.ink2)
            .buttonStyle(.plain)
            .padding(.trailing, -8)
            .padding(.vertical, 2)
        }
    }

    private func load() async {
        let currentRequest = request
        let id = UUID()
        let currentOwner = APICostOwner(account: currentRequest.account, resetAt: currentRequest.resetAt)
        if owner != currentOwner {
            selectedModel = nil
            owner = currentOwner
        }
        requestID = id
        response = nil
        loadError = nil
        loading = true
        do {
            let result = try await store.fetchAPICosts(days: currentRequest.days)
            guard id == requestID, request == currentRequest, !Task.isCancelled else { return }
            if let selectedModel, !result.models.contains(where: { $0.model == selectedModel }) {
                self.selectedModel = nil
            }
            response = result
            selectedBucket = max(0, result.buckets.count - 1)
            loading = false
        } catch is CancellationError {
            return
        } catch {
            guard id == requestID, request == currentRequest, !Task.isCancelled else { return }
            loadError = error.localizedDescription
            loading = false
        }
    }

    private func dateRange(_ response: APICostResponse) -> String {
        guard let first = response.buckets.first, let last = response.buckets.last else { return "" }
        let start = formattedDate(first.startAt, timezone: response.timezone)
        let end = formattedDate(last.startAt, timezone: response.timezone)
        return start == end ? start : "\(start) – \(end)"
    }

    private func formattedDate(_ value: String, timezone: String) -> String {
        guard let date = NoriDateFormatting.parse(value) else { return value }
        let formatter = DateFormatter()
        formatter.timeZone = TimeZone(identifier: timezone) ?? .current
        formatter.setLocalizedDateFormatFromTemplate("MMMd")
        return formatter.string(from: date)
    }

    private func bucketLabels(_ response: APICostResponse, short: Bool) -> [String] {
        let zone = TimeZone(identifier: response.timezone) ?? .current
        let formatter = DateFormatter()
        formatter.timeZone = zone
        let dates = response.buckets.map { NoriDateFormatting.parse($0.startAt) }
        if response.bucketSize == "hour" {
            var calendar = Calendar.current
            calendar.timeZone = zone
            let labels = dates.enumerated().map { index, date in
                guard let date else { return response.buckets[index].startAt }
                let minutes = calendar.component(.minute, from: date)
                formatter.setLocalizedDateFormatFromTemplate(minutes == 0 ? "j" : "jm")
                return formatter.string(from: date)
            }
            let counts = Dictionary(labels.map { ($0, 1) }, uniquingKeysWith: +)
            return labels.enumerated().map { index, label in
                guard counts[label, default: 0] > 1, let date = dates[index] else { return label }
                let seconds = zone.secondsFromGMT(for: date)
                let sign = seconds < 0 ? "-" : "+"
                let magnitude = abs(seconds)
                return "\(label) GMT\(sign)\(magnitude / 3600):\(String(format: "%02d", (magnitude % 3600) / 60))"
            }
        }
        formatter.setLocalizedDateFormatFromTemplate(short ? "MMMd" : "EEEMMMd")
        return dates.enumerated().map { index, date in
            date.map { formatter.string(from: $0) } ?? response.buckets[index].startAt
        }
    }

    private func axisIndices(_ count: Int) -> [Int] {
        guard count > 1 else { return [0] }
        return Array(Set([0, (count - 1) / 3, 2 * (count - 1) / 3, count - 1])).sorted()
    }

    private func bucketAccessibility(label: String, amount: APICostAmount) -> String {
        var text = "\(label), \(periodCost(amount))"
        if amount.unpricedCalls > 0 {
            text += ", \(callCount(amount.unpricedCalls)) without cost"
        }
        if amount.pendingCalls > 0 {
            text += ", \(callCount(amount.pendingCalls)) in progress"
        }
        return text
    }

    private func chartStep(_ peak: Double) -> Double {
        guard peak > 0 else { return 0.01 }
        let target = peak / 3
        let magnitude = pow(10, floor(log10(target)))
        let normalized = target / magnitude
        let factor: Double = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10
        return factor * magnitude
    }

    private func costString(_ value: Double) -> String {
        if value > 0 && value < 0.01 { return "<$0.01" }
        return value.formatted(.currency(code: "USD").locale(Locale(identifier: "en_US")))
    }

    private func axisCost(_ value: Double) -> String {
        if value == 0 { return "$0" }
        if value >= 1 && value.rounded() == value { return "$\(Int(value))" }
        if value >= 0.01 { return String(format: "$%.2f", value) }
        return String(format: "$%.4g", value)
    }

    private func periodCost(_ amount: APICostAmount) -> String {
        if amount.hasKnownCost { return costString(amount.costUSD) }
        if amount.unpricedCalls > 0 { return "Cost unavailable" }
        if amount.pendingCalls > 0 { return "In progress" }
        return "No calls"
    }

    private func modelCost(_ amount: APICostAmount) -> String {
        if amount.hasKnownCost { return costString(amount.costUSD) }
        if amount.unpricedCalls > 0 { return "Cost unavailable" }
        if amount.pendingCalls > 0 { return "In progress" }
        return costString(0)
    }

    private func callCount(_ count: Int) -> String {
        "\(count) \(count == 1 ? "call" : "calls")"
    }

    private func unknownMessage(_ amount: APICostAmount) -> String {
        if amount.hasKnownCost {
            return "\(callCount(amount.unpricedCalls)) with unavailable cost. The total includes priced calls only."
        }
        return "Cost is unavailable for \(callCount(amount.unpricedCalls)) in this view."
    }

    private func modelDetail(_ amount: APICostAmount) -> String {
        var details = [callCount(amount.totalCalls)]
        if amount.localCalls == amount.totalCalls && amount.localCalls > 0 {
            details.append("Local")
        } else if amount.localCalls > 0 {
            details.append("\(amount.localCalls) local")
        }
        if amount.unpricedCalls > 0 { details.append("\(amount.unpricedCalls) without cost") }
        if amount.pendingCalls > 0 { details.append("\(amount.pendingCalls) in progress") }
        return details.joined(separator: " · ")
    }

    private func modelName(_ model: String) -> String {
        guard !model.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return "Unknown model" }
        let profiles = store.modelSettings?.modelProfiles.profiles ?? store.capabilities?.modelProfiles?.profiles ?? []
        return profiles.first(where: { $0.model == model })?.label ?? model
    }
}
