import SwiftUI
import NoriCore
import NoriDesign

struct HomeConnectorSettingsView: View {
    @Bindable var store: NoriStore
    @State private var showingPairing = false
    @State private var showingDownloads = false
    @State private var selectedDevice: HomeConnector?
    @State private var busyDeviceID: String?
    @State private var errorMessage: String?
    @State private var statusMessage: String?

    var body: some View {
        Group {
          if let connectors = store.homeConnectors, connectors.enabled {
            NoriSectionHeader("Home AI Server")
            NoriCard(padding: 14) {
                VStack(alignment: .leading, spacing: 12) {
                    HStack(spacing: 10) {
                        Image(systemName: "server.rack")
                            .foregroundStyle(NoriColors.accent)
                            .frame(width: 36, height: 36)
                            .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 10))
                        Text("Connect a computer")
                            .font(NoriTypography.rowTitle)
                        Spacer()
                    }
                    HStack {
                        Button("Get connector") { showingDownloads = true }
                            .buttonStyle(NoriButtonStyle(.tonal, compact: true))
                        Button("Enter code") { showingPairing = true }
                            .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                    }
                }
            }

            ForEach(connectors.devices) { device in
                deviceCard(device)
            }
            if let errorMessage {
                NoriNotice(title: errorMessage, tone: .danger)
            }
            if let statusMessage {
                Text(statusMessage).font(NoriTypography.meta).foregroundStyle(NoriColors.ink2)
            }
          }
        }
        .sheet(isPresented: $showingPairing) {
            NavigationStack { HomePairingView(store: store) }
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
        .sheet(isPresented: $showingDownloads) {
            NavigationStack { HomeDownloadsView(downloads: store.homeConnectors?.downloads) }
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
    }

    private func deviceCard(_ device: HomeConnector) -> some View {
        NoriCard(padding: 14) {
            VStack(alignment: .leading, spacing: 12) {
                HStack {
                    VStack(alignment: .leading, spacing: 3) {
                        Text(device.name).font(NoriTypography.rowTitle)
                        if device.status != "online", let lastSeenAt = device.lastSeenAt {
                            Text("Last seen \(NoriDateFormatting.relative(lastSeenAt))")
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink2)
                        }
                    }
                    Spacer()
                    NoriPill(statusLabel(device.status), tone: device.status == "online" ? .ok : .danger)
                }
                if !device.models.isEmpty {
                    Text(device.models.map(\.name).joined(separator: " · "))
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink2)
                }
                HStack {
                    Button("Test connection") { Task { await test(device) } }
                        .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                        .disabled(busyDeviceID != nil || device.status != "online")
                    Button("Disconnect") { selectedDevice = device }
                        .buttonStyle(NoriButtonStyle(.danger, compact: true))
                        .disabled(busyDeviceID != nil)
                }
            }
        }
        .alert("Disconnect \(device.name)?", isPresented: Binding(
            get: { selectedDevice?.id == device.id },
            set: { if !$0 { selectedDevice = nil } }
        )) {
            Button("Disconnect", role: .destructive) { Task { await revoke(device) } }
            Button("Cancel", role: .cancel) { selectedDevice = nil }
        } message: {
            Text("Choose another model for any route using this computer.")
        }
    }

    private func statusLabel(_ status: String) -> String {
        switch status {
        case "online": return "Online"
        case "ollama_unavailable": return "Ollama unavailable"
        default: return "Offline"
        }
    }

    private func test(_ device: HomeConnector) async {
        busyDeviceID = device.id
        errorMessage = nil
        statusMessage = nil
        defer { busyDeviceID = nil }
        do {
            try await store.testHomeConnector(device.id)
            statusMessage = "Connection test passed."
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func revoke(_ device: HomeConnector) async {
        busyDeviceID = device.id
        errorMessage = nil
        statusMessage = nil
        defer { busyDeviceID = nil; selectedDevice = nil }
        do {
            try await store.revokeHomeConnector(device.id)
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

private struct HomePairingView: View {
    @Environment(\.dismiss) private var dismiss
    @Bindable var store: NoriStore
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
                        .autocorrectionDisabled()
                        #if os(iOS)
                        .textInputAutocapitalization(.characters)
                        #endif
                        .padding(12)
                        .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: 12))
                    Button(pending ? "Checking…" : "Continue") { Task { await lookup() } }
                        .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                        .disabled(pending || code.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
                if let errorMessage {
                    NoriNotice(title: errorMessage, tone: .danger)
                }
            }
            .padding(20)
        }
        .background(NoriColors.sheetBackground)
        .navigationTitle("Pair a computer")
        .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Close") { dismiss() }.disabled(pending) } }
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

private struct HomeDownloadsView: View {
    @Environment(\.dismiss) private var dismiss
    let downloads: HomeConnectorDownloads?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text("Install Nori Connector on the computer running Ollama, then enter the code shown there.")
                    .font(NoriTypography.body)
                    .foregroundStyle(NoriColors.ink2)
                if let downloads {
                    download("Mac", downloads.macOS)
                    download("Linux · Intel / AMD", downloads.linuxAMD64)
                    download("Linux · ARM", downloads.linuxARM64)
                    if downloads.macOS == nil && downloads.linuxAMD64 == nil && downloads.linuxARM64 == nil {
                        Text("Connector downloads are not available yet.").font(NoriTypography.meta).foregroundStyle(NoriColors.ink2)
                    }
                } else {
                    Text("Connector downloads are not available yet.").font(NoriTypography.meta).foregroundStyle(NoriColors.ink2)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(20)
        }
        .background(NoriColors.sheetBackground)
        .navigationTitle("Get Nori Connector")
        .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Close") { dismiss() } } }
    }

    @ViewBuilder
    private func download(_ label: String, _ address: String?) -> some View {
        if let address, let url = URL(string: address), url.scheme == "https", url.user == nil, url.password == nil {
            ShareLink(item: url) {
                Label("Share \(label) download", systemImage: "square.and.arrow.up")
            }
            .buttonStyle(NoriButtonStyle(.tonal, compact: true))
        }
    }
}
