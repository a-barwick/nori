import CoreTransferable
import SwiftUI
import UniformTypeIdentifiers
import NoriCore
import NoriDesign

private struct DiagnosticLogReport: Transferable {
    let metadata: NoriDiagnosticMetadata

    static var transferRepresentation: some TransferRepresentation {
        DataRepresentation(exportedContentType: .plainText) { report in
            Data(OSLogDiagnosticExporter.exportCurrentSessionReport(metadata: report.metadata).utf8)
        }
        .suggestedFileName("Nori iOS Diagnostics")
    }
}

public struct DiagnosticsSettingsView: View {
    @Bindable private var store: NoriStore
    @State private var isPingingServer = false

    public init(store: NoriStore) {
        self.store = store
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                NoriCard {
                    VStack(alignment: .leading, spacing: 10) {
                        diagRow("Status", value: store.connectionState.rawValue.capitalized)
                        diagRow("Server", value: store.serverHost)
                        if let ping = store.lastPing {
                            let pingTime = NoriDateFormatting.shortTime(ISO8601DateFormatter().string(from: ping.checkedAt))
                            let pingSummary = ping.ok
                                ? "Healthy · \(ping.latencyMs) ms · \(pingTime)"
                                : "\(ping.errorMessage ?? ping.status) · \(ping.latencyMs) ms · \(pingTime)"
                            diagRow("Health", value: pingSummary)
                        }
                        if let caps = store.capabilities {
                            let contracts = [caps.contract, caps.conversationContract, caps.assistantContract]
                                .compactMap { $0 }
                                .joined(separator: " · ")
                            diagRow("Contracts", value: contracts, monospaced: true)
                            if let provider = caps.provider, !provider.isEmpty {
                                diagRow("Provider", value: provider, monospaced: true)
                            }
                            if let model = caps.model, !model.isEmpty {
                                diagRow("Model", value: model, monospaced: true)
                            }
                            if let features = caps.assistantFeatures {
                                let enabled = features.filter(\.value).map(\.key).sorted()
                                if !enabled.isEmpty {
                                    VStack(alignment: .leading, spacing: 6) {
                                        Text("Connected capabilities")
                                            .font(.system(size: 12, weight: .medium))
                                            .foregroundStyle(NoriColors.ink3)
                                        FlowCapabilitiesView(items: enabled)
                                    }
                                    .padding(.top, 2)
                                }
                            }
                        }
                    }
                }

                telemetryAndLogsCard
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .refreshable {
            await store.refreshSummary()
        }
        .background(NoriColors.canvas)
        .navigationTitle("Diagnostics")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button(isPingingServer ? "Checking…" : "Check health") {
                    pingServerHealth()
                }
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(NoriColors.accent)
                .disabled(isPingingServer)
            }
        }
    }

    private func pingServerHealth() {
        guard !isPingingServer else { return }
        isPingingServer = true
        Task {
            defer { isPingingServer = false }
            let result = await store.pingServer()
            store.showToast(
                result.ok
                    ? "Server reachable (\(result.latencyMs) ms)"
                    : (result.errorMessage ?? "Server unreachable")
            )
        }
    }

    private var telemetryAndLogsCard: some View {
        let telemetry = NoriTelemetryRuntime.shared
        let info = Bundle.main.infoDictionary ?? [:]
        let version = (info["CFBundleShortVersionString"] as? String) ?? "1.0"
        let build = (info["CFBundleVersion"] as? String) ?? "1"
        let osVersion = ProcessInfo.processInfo.operatingSystemVersionString
        let report = DiagnosticLogReport(
            metadata: NoriDiagnosticMetadata(
                appVersion: version,
                buildNumber: build,
                osVersion: osVersion,
                connectionState: store.connectionState.rawValue,
                userID: telemetry.currentUserID,
                lastTraceID: telemetry.lastTraceID,
                sentryEnabled: telemetry.isEnabled,
                sentryEnvironment: telemetry.environmentName
            )
        )

        return NoriCard {
            VStack(alignment: .leading, spacing: 10) {
                Text("Telemetry & Logs")
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)

                diagRow("Build", value: "com.nori.ios@\(version)+\(build)", monospaced: true)
                diagRow(
                    "Sentry",
                    value: telemetry.isEnabled ? "Enabled (\(telemetry.environmentName))" : "Disabled (\(telemetry.environmentName))"
                )
                if let uid = telemetry.currentUserID, !uid.isEmpty {
                    diagRow("User ID", value: uid, monospaced: true)
                }
                if let traceID = telemetry.lastTraceID, !traceID.isEmpty {
                    diagRow("Last Trace ID", value: traceID, monospaced: true)
                }

                HStack(spacing: 10) {
                    ShareLink(
                        item: report,
                        preview: SharePreview("Nori iOS Diagnostics")
                    ) {
                        Label("Export Diagnostic Logs", systemImage: "square.and.arrow.up")
                            .font(.system(size: 13.5, weight: .semibold))
                            .foregroundStyle(NoriColors.accent)
                    }

                    Spacer()

                    #if DEBUG
                    Button("Send Test Event") {
                        let testSpan = telemetry.startSpan(name: "nori.diagnostics.test", operation: "test")
                        NoriTelemetryRuntime.$parentSpan.withValue(testSpan) {
                            NoriLogger.telemetry.warning("diagnostics.test_event")
                            telemetry.captureError(
                                NoriAPIError(message: "Diagnostic test event from iOS Settings"),
                                category: "diagnostics.test"
                            )
                        }
                        testSpan.finish(status: .ok)
                        store.showToast("Test event queued")
                    }
                    .disabled(!telemetry.isEnabled)
                    .font(.system(size: 13, weight: .medium))
                    .foregroundStyle(NoriColors.ink2)
                    #endif
                }
                .padding(.top, 4)
            }
        }
    }

    @ViewBuilder
    private func diagRow(_ label: String, value: String, monospaced: Bool = false) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Text(label)
                .font(.system(size: 12.5, weight: .medium))
                .foregroundStyle(NoriColors.ink3)
                .frame(width: 76, alignment: .leading)
            Text(value)
                .font(monospaced ? NoriTypography.monoSmall : .system(size: 13, weight: .medium))
                .foregroundStyle(NoriColors.ink)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

private struct FlowCapabilitiesView: View {
    let items: [String]

    var body: some View {
        let columns = [GridItem(.adaptive(minimum: 82), spacing: 6)]
        LazyVGrid(columns: columns, alignment: .leading, spacing: 6) {
            ForEach(items, id: \.self) { name in
                NoriPill(name, tone: .accent)
            }
        }
    }
}
