import AppKit
import Combine
import Foundation
import ServiceManagement
import SwiftUI

struct ConnectorModel: Decodable, Identifiable {
    let name: String
    let digest: String

    var id: String { name }
}

struct ConnectorStatus: Decodable {
    let state: String
    let paired: Bool
    let ollama_available: Bool
    let models: [ConnectorModel]?
}

struct PairingEvent: Decodable {
    let type: String
    let user_code: String?
    let verification_uri: String?
}

final class PairingBuffer: @unchecked Sendable {
    private var data = Data()
    private let lock = NSLock()

    func append(_ input: Data) -> [PairingEvent] {
        lock.lock()
        defer { lock.unlock() }
        data.append(input)
        var events: [PairingEvent] = []
        while let index = data.firstIndex(of: 10) {
            let line = Data(data[..<index])
            data.removeSubrange(...index)
            if let event = try? JSONDecoder().decode(PairingEvent.self, from: line) {
                events.append(event)
            }
        }
        return events
    }
}

@MainActor
final class ConnectorViewModel: ObservableObject {
    @Published var status = ConnectorStatus(state: "stopped", paired: false, ollama_available: false, models: [])
    @Published var code: String?
    @Published var verificationURL: URL?
    @Published var error: String?
    @Published var pairing = false
    @Published var checking = false
    @Published var background = false

    private let agent = SMAppService.agent(plistName: "com.nori.connector.agent.plist")
    private var pairingProcess: Process?
    private var statusInProgress = false

    init() {
        updateBackground()
        refresh()
    }

    private var executable: URL {
        Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/nori-connector")
    }

    func refresh() {
        updateBackground()
        guard !statusInProgress else { return }
        statusInProgress = true
        run(arguments: ["status", "--json"]) { [weak self] data, message, exit in
            guard let self else { return }
            self.statusInProgress = false
            if exit == 0, let value = try? JSONDecoder().decode(ConnectorStatus.self, from: data) {
                self.status = value
            } else if !message.isEmpty {
                self.error = message
            }
        }
    }

    func setBackground(_ enabled: Bool) {
        error = nil
        do {
            if enabled {
                try agent.register()
            } else {
                try agent.unregister()
            }
        } catch {
            self.error = error.localizedDescription
        }
        updateBackground()
    }

    private func updateBackground() {
        background = agent.status == .enabled
    }

    func pair() {
        error = nil
        if !background {
            setBackground(true)
            if !background {
                if agent.status == .requiresApproval {
                    error = "Allow Nori Connector in System Settings → General → Login Items."
                }
                return
            }
        }
        let process = Process()
        process.executableURL = executable
        process.arguments = ["pair", "--json"]
        let output = Pipe()
        let errors = Pipe()
        process.standardOutput = output
        process.standardError = errors
        let buffer = PairingBuffer()
        output.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            if data.isEmpty {
                handle.readabilityHandler = nil
                return
            }
            for event in buffer.append(data) {
                DispatchQueue.main.async {
                    guard let self, self.pairingProcess === process else { return }
                    if event.type == "pairing" {
                        self.code = event.user_code
                        self.verificationURL = event.verification_uri.flatMap(URL.init(string:))
                    }
                }
            }
        }
        process.terminationHandler = { [weak self] process in
            let data = errors.fileHandleForReading.readDataToEndOfFile()
            let message = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            DispatchQueue.main.async {
                guard let self, self.pairingProcess === process else { return }
                self.pairing = false
                self.pairingProcess = nil
                if process.terminationStatus == 0 {
                    self.code = nil
                    self.verificationURL = nil
                    self.refresh()
                } else if !message.isEmpty {
                    self.error = message
                }
            }
        }
        do {
            try process.run()
            pairingProcess = process
            pairing = true
        } catch {
            self.error = error.localizedDescription
        }
    }

    func cancelPairing() {
        pairingProcess?.terminate()
        pairingProcess = nil
        pairing = false
        code = nil
        verificationURL = nil
    }

    func checkOllama() {
        checking = true
        run(arguments: ["doctor", "--json"]) { [weak self] data, message, exit in
            guard let self else { return }
            self.checking = false
            if exit != 0 {
                self.error = message.isEmpty ? "Ollama is unavailable." : message
            } else {
                self.error = nil
                self.refresh()
            }
        }
    }

    func unpair() {
        run(arguments: ["unpair", "--json"]) { [weak self] _, message, exit in
            guard let self else { return }
            if exit == 0 {
                self.error = nil
                self.refresh()
            } else {
                self.error = message.isEmpty ? "Unable to disconnect." : message
            }
        }
    }

    private func run(arguments: [String], completion: @escaping (Data, String, Int32) -> Void) {
        let executable = executable
        DispatchQueue.global(qos: .utility).async {
            let process = Process()
            process.executableURL = executable
            process.arguments = arguments
            let output = Pipe()
            let errors = Pipe()
            process.standardOutput = output
            process.standardError = errors
            do {
                try process.run()
                let data = output.fileHandleForReading.readDataToEndOfFile()
                let errorData = errors.fileHandleForReading.readDataToEndOfFile()
                process.waitUntilExit()
                let message = String(data: errorData, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                DispatchQueue.main.async { completion(data, message, process.terminationStatus) }
            } catch {
                DispatchQueue.main.async { completion(Data(), error.localizedDescription, 1) }
            }
        }
    }
}

struct ContentView: View {
    @StateObject private var model = ConnectorViewModel()
    @State private var confirmingDisconnect = false
    private let timer = Timer.publish(every: 5, on: .main, in: .common).autoconnect()

    var body: some View {
        ScrollView {
          VStack(alignment: .leading, spacing: 16) {
            Image(systemName: "externaldrive.connected.to.line.below")
                .font(.system(size: 26, weight: .medium))
                .foregroundStyle(.purple)
                .padding(12)
                .background(.purple.opacity(0.1), in: RoundedRectangle(cornerRadius: 14))
            Text(model.status.paired ? "Home AI Server" : "Pair this Mac")
                .font(.title2.bold())
            if let code = model.code {
                Text("In Nori, open More → Models → Enter code.")
                    .foregroundStyle(.secondary)
                Text(code)
                    .font(.system(.title2, design: .monospaced).bold())
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity)
                    .padding(14)
                    .background(.quaternary, in: RoundedRectangle(cornerRadius: 12))
                HStack {
                    if let url = model.verificationURL {
                        Link("Open Nori", destination: url)
                            .buttonStyle(.borderedProminent)
                    }
                    Button("Cancel") { model.cancelPairing() }
                }
            } else if model.status.paired {
                GroupBox {
                    VStack(alignment: .leading, spacing: 10) {
                        statusRow("Nori", value: connectionText)
                        Divider()
                        statusRow("Ollama", value: model.status.ollama_available ? "Available" : "Unavailable")
                        if let models = model.status.models, !models.isEmpty {
                            Divider()
                            Text("Models").font(.subheadline.weight(.semibold))
                            ForEach(models) { item in
                                Text(item.name).font(.subheadline).foregroundStyle(.secondary)
                            }
                        }
                    }
                    .padding(5)
                }
                HStack {
                    Button("Check Ollama") { model.checkOllama() }
                        .disabled(model.checking)
                    Button("Disconnect", role: .destructive) { confirmingDisconnect = true }
                }
            } else {
                Text("Use the models installed in Ollama with your Nori account.")
                    .foregroundStyle(.secondary)
                Button(model.pairing ? "Waiting for approval…" : "Get pairing code") { model.pair() }
                    .buttonStyle(.borderedProminent)
                    .disabled(model.pairing)
            }
            Toggle("Start at login", isOn: Binding(
                get: { model.background },
                set: { model.setBackground($0) }
            ))
            if let error = model.error {
                Text(error).font(.callout).foregroundStyle(.red).textSelection(.enabled)
            }
          }
          .padding(24)
          .frame(maxWidth: .infinity, alignment: .leading)
        }
        .frame(minWidth: 380, idealWidth: 430, minHeight: 310, alignment: .topLeading)
        .onReceive(timer) { _ in model.refresh() }
        .alert("Disconnect this computer?", isPresented: $confirmingDisconnect) {
            Button("Cancel", role: .cancel) {}
            Button("Disconnect", role: .destructive) { model.unpair() }
        } message: {
            Text("Choose another model in Nori after disconnecting.")
        }
    }

    private var connectionText: String {
        switch model.status.state {
        case "online": return "Online"
        case "connecting": return "Connecting"
        case "unpaired": return "Disconnected"
        default: return "Stopped"
        }
    }

    private func statusRow(_ title: String, value: String) -> some View {
        HStack {
            Text(title).fontWeight(.medium)
            Spacer()
            Text(value).foregroundStyle(.secondary)
        }
    }
}

@main
struct NoriConnectorApp: App {
    var body: some Scene {
        WindowGroup("Nori Connector") {
            ContentView()
        }
        .windowResizability(.contentMinSize)
    }
}
