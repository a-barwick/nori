import SwiftUI
import NoriCore
import NoriDesign

struct HomeConnectorSettingsView: View {
    @Bindable var store: NoriStore
    var loadError: String?
    var onRetry: (() -> Void)?

    @State private var showingPairing = false

    var body: some View {
        Group {
            if let connectors = store.homeConnectors, connectors.enabled {
                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Home computer") {
                        Button("+ Pair") { showingPairing = true }
                            .font(.system(size: 12.5, weight: .semibold))
                            .foregroundStyle(NoriColors.accent)
                            .buttonStyle(.plain)
                    }

                    if let loadError {
                        NoriNotice(title: "Could not refresh home computers", detail: loadError, tone: .danger) {
                            if let onRetry {
                                Button("Retry", action: onRetry)
                                    .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                            }
                        }
                        .padding(.bottom, 10)
                    }

                    if connectors.devices.isEmpty {
                        NoriCard {
                            Text("No home computer paired.")
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink2)
                        }
                    } else {
                        NoriRows {
                            ForEach(Array(connectors.devices.enumerated()), id: \.element.id) { index, device in
                                let isLast = index == connectors.devices.count - 1
                                NavigationLink {
                                    HomeDeviceDetailView(store: store, device: device)
                                } label: {
                                    NoriBrandRow(
                                        title: device.name,
                                        brand: .homeComputer,
                                        showChevron: true,
                                        showDivider: !isLast
                                    ) {
                                        if device.status != "online" {
                                            NoriPill(
                                                statusLabel(device.status),
                                                tone: device.status == "ollama_unavailable" ? .warning : .danger
                                            )
                                        }
                                    }
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                }
            } else if let loadError {
                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Home computer")
                    NoriNotice(title: "Could not load home computers", detail: loadError, tone: .danger) {
                        if let onRetry {
                            Button("Retry", action: onRetry)
                                .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        }
                    }
                }
            }
        }
        .sheet(isPresented: $showingPairing) {
            NavigationStack {
                HomePairingView(store: store, downloads: store.homeConnectors?.downloads)
            }
            .presentationDetents([.medium, .large])
            .presentationDragIndicator(.visible)
            .presentationBackground(NoriColors.sheetBackground)
            .presentationCornerRadius(NoriRadius.sheet)
        }
    }

    private func statusLabel(_ status: String) -> String {
        switch status {
        case "online": return "Online"
        case "ollama_unavailable": return "Ollama unavailable"
        default: return "Offline"
        }
    }
}

private struct HomeDeviceDetailView: View {
    @Bindable var store: NoriStore
    let device: HomeConnector

    @Environment(\.dismiss) private var dismiss
    @State private var isBusy = false
    @State private var statusMessage: String?
    @State private var errorMessage: String?
    @State private var showingDisconnect = false

    private var liveDevice: HomeConnector {
        store.homeConnectors?.devices.first { $0.id == device.id } ?? device
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if let errorMessage {
                    NoriNotice(title: errorMessage, tone: .danger)
                }
                if let statusMessage {
                    NoriNotice(title: statusMessage, tone: .ok)
                }

                NoriRows {
                    Button {
                        Task { await test() }
                    } label: {
                        NoriRow(
                            title: isBusy ? "Testing…" : "Test connection",
                            showChevron: true,
                            showDivider: false
                        )
                    }
                    .buttonStyle(.plain)
                    .disabled(isBusy || liveDevice.status != "online")
                }

                if !liveDevice.models.isEmpty {
                    VStack(alignment: .leading, spacing: 0) {
                        NoriSectionHeader("Available models")
                        NoriRows {
                            ForEach(Array(liveDevice.models.enumerated()), id: \.element.name) { index, model in
                                NoriRow(
                                    title: model.name,
                                    showDivider: index < liveDevice.models.count - 1
                                )
                            }
                        }
                    }
                }

                Button {
                    showingDisconnect = true
                } label: {
                    Text("Disconnect \(liveDevice.name)")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(NoriButtonStyle(.danger, fullWidth: true))
                .disabled(isBusy)
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle(liveDevice.name)
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .alert("Disconnect \(liveDevice.name)?", isPresented: $showingDisconnect) {
            Button("Disconnect", role: .destructive) { Task { await revoke() } }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Choose another model for Chat or Work before disconnecting.")
        }
    }

    private func test() async {
        isBusy = true
        errorMessage = nil
        statusMessage = nil
        defer { isBusy = false }
        do {
            try await store.testHomeConnector(liveDevice.id)
            statusMessage = "Connection test passed."
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func revoke() async {
        isBusy = true
        errorMessage = nil
        defer { isBusy = false }
        do {
            try await store.revokeHomeConnector(liveDevice.id)
            dismiss()
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

private struct HomePairingView: View {
    @Environment(\.dismiss) private var dismiss
    @Bindable var store: NoriStore
    let downloads: HomeConnectorDownloads?
    @State private var code = ""
    @State private var pairing: HomePairing?
    @State private var pending = false
    @State private var errorMessage: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if let pairing {
                    Text("Pair \(pairing.name)?")
                        .font(NoriTypography.section)
                        .foregroundStyle(NoriColors.ink)
                    Text("Confirm that \(pairing.userCode) matches the code on your computer. Nori sends model requests to this computer when you select a home model.")
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink2)
                    Button(pending ? "Pairing…" : "Pair device") { Task { await approve(pairing) } }
                        .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                        .disabled(pending)
                    Button("Back") { self.pairing = nil }
                        .buttonStyle(NoriButtonStyle(.quiet, fullWidth: true))
                        .disabled(pending)
                } else {
                    Text("Enter the code shown by Nori Connector.")
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink2)
                    TextField("Device code", text: $code)
                        .textFieldStyle(.plain)
                        .font(NoriTypography.mono)
                        .foregroundStyle(NoriColors.ink)
                        .tint(NoriColors.accent)
                        .autocorrectionDisabled()
                        #if os(iOS)
                        .textInputAutocapitalization(.characters)
                        #endif
                        .padding(.horizontal, 12)
                        .frame(height: 44)
                        .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    Button(pending ? "Checking…" : "Continue") { Task { await lookup() } }
                        .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                        .disabled(pending || code.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)

                    if let downloads {
                        VStack(alignment: .leading, spacing: 8) {
                            NoriSectionHeader("Get Nori Connector")
                            download("Download for Mac", downloads.macOS)
                            download("Download for Linux · Intel / AMD", downloads.linuxAMD64)
                            download("Download for Linux · ARM", downloads.linuxARM64)
                        }
                        .padding(.top, 8)
                    }
                }
                if let errorMessage {
                    NoriNotice(title: errorMessage, tone: .danger)
                }
            }
            .padding(20)
        }
        .background(NoriColors.sheetBackground)
        .navigationTitle("Pair computer")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Close") { dismiss() }.disabled(pending) } }
        .interactiveDismissDisabled(pending)
    }

    @ViewBuilder
    private func download(_ label: String, _ address: String?) -> some View {
        if let address, let url = URL(string: address), url.scheme == "https", url.user == nil, url.password == nil {
            ShareLink(item: url) {
                Label(label, systemImage: "square.and.arrow.up")
            }
            .buttonStyle(NoriButtonStyle(.tonal, compact: true))
        }
    }

    private func lookup() async {
        pending = true
        errorMessage = nil
        defer { pending = false }
        do {
            pairing = try await store.lookupHomePairing(code.trimmingCharacters(in: .whitespacesAndNewlines))
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func approve(_ pairing: HomePairing) async {
        pending = true
        errorMessage = nil
        defer { pending = false }
        do {
            try await store.approveHomePairing(pairing.userCode)
            dismiss()
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}
