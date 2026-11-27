import SwiftUI
import NoriCore
import NoriDesign

public struct ComputerDesktopSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Bindable private var store: NoriStore
    private let initialMode: ComputerSessionMode
    private let taskID: String?
    private let interactionID: String?
    private let prompt: String?
    private let onDoneContinue: (() async -> Void)?

    @State private var client: ComputerRFBClient
    @State private var session: CreateComputerSessionResponse?
    @State private var mode: ComputerSessionMode
    @State private var isSwitchingMode: Bool = false
    @State private var isCompleting: Bool = false
    @State private var errorMessage: String?
    @State private var keyboardDraft: String = ""
    @State private var isSecureInput: Bool = true

    public init(
        store: NoriStore,
        initialMode: ComputerSessionMode = .view,
        taskID: String? = nil,
        interactionID: String? = nil,
        prompt: String? = nil,
        onDoneContinue: (() async -> Void)? = nil
    ) {
        self.store = store
        self.initialMode = initialMode
        self.taskID = taskID
        self.interactionID = interactionID
        self.prompt = prompt
        self.onDoneContinue = onDoneContinue
        self._mode = State(initialValue: initialMode)
        self._client = State(initialValue: ComputerRFBClient(viewOnly: initialMode == .view))
    }

    private var statusPill: (label: String, tone: NoriTone) {
        switch client.state {
        case .connected:
            return mode == .control ? ("In control", .accent) : ("Watching", .ok)
        case .connecting:
            return ("Starting desktop…", .warning)
        case .reconnecting:
            return ("Reconnecting…", .warning)
        case .error:
            return ("Disconnected", .danger)
        case .disconnected:
            return ("Disconnected", .neutral)
        }
    }

    public var body: some View {
        VStack(spacing: 0) {
            headerBar

            if let displayError = errorMessage ?? client.errorMessage {
                NoriNotice(title: displayError, tone: .danger, systemImage: "exclamationmark.triangle.fill")
                    .padding(.horizontal, 16)
                    .padding(.top, 10)
            }

            desktopCanvas
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Color.black)

            if mode == .control {
                keyboardInputBar
            }

            footerBar
        }
        .background(NoriColors.sheetBackground)
        .task {
            await startSession()
        }
        .onDisappear {
            client.disconnect()
            if let active = session {
                session = nil
                Task { await store.endComputerSession(sessionID: active.id) }
            }
        }
    }

    private var headerBar: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 10) {
                Image(systemName: "desktopcomputer")
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(NoriColors.accent)
                    .frame(width: 30, height: 30)
                    .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 9, style: .continuous))

                VStack(alignment: .leading, spacing: 2) {
                    Text("Nori Computer")
                        .font(.system(size: 15.5, weight: .semibold))
                        .foregroundStyle(NoriColors.ink)
                    if let prompt, !prompt.isEmpty {
                        Text(prompt)
                            .font(.system(size: 12.5))
                            .foregroundStyle(NoriColors.ink2)
                            .lineLimit(2)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                NoriPill(statusPill.label, tone: statusPill.tone)

                Button {
                    dismiss()
                } label: {
                    Image(systemName: "xmark")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(NoriColors.ink2)
                        .frame(width: 30, height: 30)
                        .background(NoriColors.surface, in: Circle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Close desktop")
            }

            HStack(spacing: 8) {
                Button {
                    toggleMode()
                } label: {
                    HStack(spacing: 5) {
                        Image(systemName: mode == .control ? "eye" : "hand.tap")
                            .font(.system(size: 12, weight: .semibold))
                        Text(mode == .control ? "Hand back to Nori" : "Take control")
                    }
                }
                .buttonStyle(NoriButtonStyle(mode == .control ? .neutral : .primary, compact: true))
                .disabled(isSwitchingMode || client.state != .connected)

                if onDoneContinue != nil {
                    Button {
                        completeTakeover()
                    } label: {
                        HStack(spacing: 5) {
                            Image(systemName: "checkmark")
                                .font(.system(size: 12, weight: .semibold))
                            Text(isCompleting ? "Resuming…" : "Done, continue")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, compact: true))
                    .disabled(isCompleting)
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .overlay(alignment: .bottom) {
            Rectangle().fill(NoriColors.hairline).frame(height: 1)
        }
    }

    private var desktopCanvas: some View {
        GeometryReader { geo in
            ZStack {
                if let image = client.currentImage {
                    let fitted = fittedRect(
                        contentSize: CGSize(width: client.framebufferWidth, height: client.framebufferHeight),
                        in: geo.size
                    )
                    Image(decorative: image, scale: 1.0)
                        .resizable()
                        .interpolation(.medium)
                        .frame(width: fitted.width, height: fitted.height)
                        .position(x: fitted.midX, y: fitted.midY)
                        .contentShape(Rectangle())
                        .gesture(
                            DragGesture(minimumDistance: 0)
                                .onChanged { value in
                                    guard mode == .control else { return }
                                    let pt = mapPoint(value.location, in: fitted)
                                    client.sendPointer(x: pt.x, y: pt.y, buttonMask: 1)
                                }
                                .onEnded { value in
                                    guard mode == .control else { return }
                                    let pt = mapPoint(value.location, in: fitted)
                                    client.sendPointer(x: pt.x, y: pt.y, buttonMask: 0)
                                }
                        )
                } else {
                    VStack(spacing: 10) {
                        ProgressView()
                            .tint(.white)
                        Text(client.state == .error ? "Desktop unavailable" : "Waking your computer…")
                            .font(NoriTypography.meta)
                            .foregroundStyle(.white.opacity(0.8))
                    }
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private var keyboardInputBar: some View {
        HStack(spacing: 8) {
            Button {
                isSecureInput.toggle()
            } label: {
                Image(systemName: isSecureInput ? "lock.fill" : "lock.open")
                    .font(.system(size: 12.5, weight: .semibold))
                    .foregroundStyle(isSecureInput ? NoriColors.accent : NoriColors.ink3)
            }
            .buttonStyle(NoriButtonStyle(.neutral, compact: true))
            .accessibilityLabel(isSecureInput ? "Private input enabled" : "Show typed characters")

            Group {
                if isSecureInput {
                    SecureField("Type privately into desktop…", text: $keyboardDraft)
                } else {
                    TextField("Type into desktop…", text: $keyboardDraft)
                }
            }
            .font(.system(size: 14))
            .foregroundStyle(NoriColors.ink)
            .autocorrectionDisabled(true)
            #if os(iOS)
            .textInputAutocapitalization(.never)
            #endif
            .submitLabel(.send)
            .onSubmit {
                sendTypedText(pressReturn: true)
            }

            Button {
                client.sendSpecialKey(0xFF08) // BackSpace
            } label: {
                Image(systemName: "delete.left")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(NoriColors.ink2)
            }
            .buttonStyle(NoriButtonStyle(.neutral, compact: true))
            .accessibilityLabel("Backspace")

            Button("Send") {
                sendTypedText(pressReturn: false)
            }
            .buttonStyle(NoriButtonStyle(.primary, compact: true))
            .disabled(keyboardDraft.isEmpty)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .background(NoriColors.surface)
        .overlay(alignment: .top) {
            Rectangle().fill(NoriColors.hairline).frame(height: 1)
        }
    }

    private var footerBar: some View {
        HStack {
            Text(
                mode == .control
                    ? "Tap the screen or type below. Your sign-ins stay saved."
                    : "Read-only view. Choose Take control to tap or sign in."
            )
            .font(.system(size: 12))
            .foregroundStyle(NoriColors.ink2)
            Spacer(minLength: 8)
            Text("Auto-sleeps")
                .font(.system(size: 11.5))
                .foregroundStyle(NoriColors.ink3)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .overlay(alignment: .top) {
            Rectangle().fill(NoriColors.hairline).frame(height: 1)
        }
    }

    private func startSession() async {
        errorMessage = nil
        do {
            let created = try await store.createComputerSession(
                mode: initialMode,
                taskID: taskID,
                interactionID: interactionID
            )
            session = created
            mode = created.mode
            client.viewOnly = (created.mode == .view)
            let wsRequest = try await store.computerDesktopWebSocketRequest(for: created)
            client.connect(request: wsRequest) { [weak store] in
                guard let store else { throw CancellationError() }
                let refreshed = try await store.createComputerSession(
                    mode: mode,
                    taskID: taskID,
                    interactionID: interactionID
                )
                session = refreshed
                return try await store.computerDesktopWebSocketRequest(for: refreshed)
            }
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func toggleMode() {
        guard let active = session, !isSwitchingMode else { return }
        let nextMode: ComputerSessionMode = (mode == .view) ? .control : .view
        isSwitchingMode = true
        errorMessage = nil
        Task {
            do {
                _ = try await store.updateComputerSessionMode(sessionID: active.id, mode: nextMode)
                mode = nextMode
                client.viewOnly = (nextMode == .view)
            } catch {
                errorMessage = error.localizedDescription
            }
            isSwitchingMode = false
        }
    }

    private func completeTakeover() {
        guard let onDoneContinue, !isCompleting else { return }
        isCompleting = true
        Task {
            await onDoneContinue()
            isCompleting = false
            dismiss()
        }
    }

    private func sendTypedText(pressReturn: Bool) {
        let text = keyboardDraft
        keyboardDraft = ""
        client.sendText(text, pressReturn: pressReturn)
    }

    private func fittedRect(contentSize: CGSize, in containerSize: CGSize) -> CGRect {
        guard contentSize.width > 0, contentSize.height > 0,
              containerSize.width > 0, containerSize.height > 0 else {
            return CGRect(origin: .zero, size: containerSize)
        }
        let scale = min(containerSize.width / contentSize.width, containerSize.height / contentSize.height)
        let w = contentSize.width * scale
        let h = contentSize.height * scale
        return CGRect(
            x: (containerSize.width - w) / 2,
            y: (containerSize.height - h) / 2,
            width: w,
            height: h
        )
    }

    private func mapPoint(_ point: CGPoint, in fitted: CGRect) -> (x: Int, y: Int) {
        guard fitted.width > 0, fitted.height > 0 else { return (0, 0) }
        let rx = max(0, min(1, (point.x - fitted.minX) / fitted.width))
        let ry = max(0, min(1, (point.y - fitted.minY) / fitted.height))
        return (
            Int(round(rx * Double(client.framebufferWidth - 1))),
            Int(round(ry * Double(client.framebufferHeight - 1)))
        )
    }
}
