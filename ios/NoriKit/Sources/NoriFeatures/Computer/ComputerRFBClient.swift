import Foundation
import CoreGraphics
import Observation

@Observable
@MainActor
public final class ComputerRFBClient {
    public enum ConnectionState: String, Sendable, Equatable {
        case connecting
        case connected
        case reconnecting
        case disconnected
        case error
    }

    private static let maxFramebufferWidth = 2560
    private static let maxFramebufferHeight = 1600
    private static let maxBufferedBytes = 16 * 1024 * 1024
    private static let maxServerCutTextBytes = 1 * 1024 * 1024
    private static let maxReconnectAttempts = 4

    public private(set) var state: ConnectionState = .connecting
    public private(set) var errorMessage: String?
    public private(set) var framebufferWidth: Int = 1280
    public private(set) var framebufferHeight: Int = 800
    public private(set) var currentImage: CGImage?
    public var viewOnly: Bool

    private var socketTask: URLSessionWebSocketTask?
    private var receiveTask: Task<Void, Never>?
    private var reconnectTask: Task<Void, Never>?
    private var requestBuilder: (() async throws -> URLRequest)?
    private var reconnectAttempt: Int = 0
    private var manuallyDisconnected: Bool = false
    private var buffer = Data()
    private var framebuffer = Data()

    private enum Phase: Equatable {
        case version
        case securityCount
        case securityTypes(Int)
        case securityResult
        case serverInit
        case serverName(Int)
        case running
    }

    private var phase: Phase = .version
    private var pendingRects: Int = 0
    private var serverCutTextRemaining: Int = 0

    public init(viewOnly: Bool = true) {
        self.viewOnly = viewOnly
    }

    public func connect(request: URLRequest, requestBuilder: (() async throws -> URLRequest)? = nil) {
        self.requestBuilder = requestBuilder
        self.reconnectAttempt = 0
        self.manuallyDisconnected = false
        openSocket(request: request)
    }

    private func openSocket(request: URLRequest) {
        receiveTask?.cancel()
        receiveTask = nil
        reconnectTask?.cancel()
        reconnectTask = nil
        socketTask?.cancel(with: .goingAway, reason: nil)
        socketTask = nil

        state = reconnectAttempt > 0 ? .reconnecting : .connecting
        errorMessage = nil
        buffer.removeAll(keepingCapacity: true)
        phase = .version
        pendingRects = 0
        serverCutTextRemaining = 0

        let task = URLSession.shared.webSocketTask(with: request)
        socketTask = task
        task.resume()

        receiveTask = Task { [weak self] in
            guard let self else { return }
            await self.receiveLoop(task: task)
        }
    }

    public func disconnect() {
        manuallyDisconnected = true
        reconnectTask?.cancel()
        reconnectTask = nil
        receiveTask?.cancel()
        receiveTask = nil
        socketTask?.cancel(with: .goingAway, reason: nil)
        socketTask = nil
        if state != .error {
            state = .disconnected
        }
    }

    public func sendPointer(x: Int, y: Int, buttonMask: UInt8) {
        guard !viewOnly, phase == .running, let socketTask else { return }
        let clampedX = UInt16(max(0, min(framebufferWidth - 1, x)))
        let clampedY = UInt16(max(0, min(framebufferHeight - 1, y)))
        var payload = Data(count: 6)
        payload[0] = 5 // PointerEvent
        payload[1] = buttonMask
        payload[2] = UInt8((clampedX >> 8) & 0xff)
        payload[3] = UInt8(clampedX & 0xff)
        payload[4] = UInt8((clampedY >> 8) & 0xff)
        payload[5] = UInt8(clampedY & 0xff)
        socketTask.send(.data(payload)) { _ in }
    }

    public func sendClick(x: Int, y: Int) {
        sendPointer(x: x, y: y, buttonMask: 1)
        sendPointer(x: x, y: y, buttonMask: 0)
    }

    public func sendKey(keysym: UInt32, down: Bool) {
        guard !viewOnly, phase == .running, let socketTask else { return }
        var payload = Data(count: 8)
        payload[0] = 4 // KeyEvent
        payload[1] = down ? 1 : 0
        payload[4] = UInt8((keysym >> 24) & 0xff)
        payload[5] = UInt8((keysym >> 16) & 0xff)
        payload[6] = UInt8((keysym >> 8) & 0xff)
        payload[7] = UInt8(keysym & 0xff)
        socketTask.send(.data(payload)) { _ in }
    }

    public func sendText(_ text: String, pressReturn: Bool = false) {
        guard !viewOnly, phase == .running else { return }
        for scalar in text.unicodeScalars {
            let value = scalar.value
            let keysym: UInt32
            if value == 0x0A || value == 0x0D {
                keysym = 0xFF0D
            } else if value == 0x09 {
                keysym = 0xFF09
            } else if value >= 0x20 && value <= 0x7E {
                keysym = value
            } else {
                keysym = 0x0100_0000 | value
            }
            sendKey(keysym: keysym, down: true)
            sendKey(keysym: keysym, down: false)
        }
        if pressReturn {
            sendKey(keysym: 0xFF0D, down: true)
            sendKey(keysym: 0xFF0D, down: false)
        }
    }

    public func sendSpecialKey(_ keysym: UInt32) {
        sendKey(keysym: keysym, down: true)
        sendKey(keysym: keysym, down: false)
    }

    private func receiveLoop(task: URLSessionWebSocketTask) async {
        while !Task.isCancelled {
            do {
                let message = try await task.receive()
                guard socketTask === task else { return }
                switch message {
                case .data(let data):
                    if buffer.count + data.count > Self.maxBufferedBytes {
                        failAndDisconnect("Desktop stream exceeded memory limit.")
                        return
                    }
                    buffer.append(data)
                    processBuffer()
                case .string(let str):
                    if let data = str.data(using: .utf8) {
                        if buffer.count + data.count > Self.maxBufferedBytes {
                            failAndDisconnect("Desktop stream exceeded memory limit.")
                            return
                        }
                        buffer.append(data)
                        processBuffer()
                    }
                @unknown default:
                    break
                }
            } catch {
                guard socketTask === task, !Task.isCancelled, !manuallyDisconnected else { return }
                scheduleReconnect()
                return
            }
        }
    }

    private func failAndDisconnect(_ message: String) {
        manuallyDisconnected = true
        state = .error
        errorMessage = message
        disconnect()
    }

    private func scheduleReconnect() {
        guard let requestBuilder, reconnectAttempt < Self.maxReconnectAttempts else {
            state = .error
            errorMessage = "Desktop connection closed."
            return
        }
        reconnectAttempt += 1
        state = .reconnecting
        let delaySeconds = min(8, 1 << (reconnectAttempt - 1))
        reconnectTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delaySeconds))
            guard let self, !Task.isCancelled, !self.manuallyDisconnected else { return }
            do {
                let freshRequest = try await requestBuilder()
                guard !Task.isCancelled, !self.manuallyDisconnected else { return }
                self.openSocket(request: freshRequest)
            } catch {
                self.scheduleReconnect()
            }
        }
    }

    private func sendRaw(_ data: Data) {
        socketTask?.send(.data(data)) { _ in }
    }

    private func sendPixelFormatAndEncodings() {
        var pf = Data(count: 20)
        pf[0] = 0 // SetPixelFormat
        pf[4] = 32 // bpp
        pf[5] = 24 // depth
        pf[6] = 0  // little-endian
        pf[7] = 1  // true-color
        pf[8] = 0; pf[9] = 255   // red-max
        pf[10] = 0; pf[11] = 255 // green-max
        pf[12] = 0; pf[13] = 255 // blue-max
        pf[14] = 0  // red-shift
        pf[15] = 8  // green-shift
        pf[16] = 16 // blue-shift
        sendRaw(pf)

        var enc = Data(count: 16)
        enc[0] = 2 // SetEncodings
        enc[2] = 0; enc[3] = 3 // 3 encodings
        writeInt32(&enc, offset: 4, value: 0)    // Raw
        writeInt32(&enc, offset: 8, value: 1)    // CopyRect
        writeInt32(&enc, offset: 12, value: -223) // DesktopSize
        sendRaw(enc)
    }

    private func requestFramebufferUpdate(incremental: Bool) {
        var req = Data(count: 10)
        req[0] = 3
        req[1] = incremental ? 1 : 0
        req[2] = 0; req[3] = 0
        req[4] = 0; req[5] = 0
        req[6] = UInt8((framebufferWidth >> 8) & 0xff)
        req[7] = UInt8(framebufferWidth & 0xff)
        req[8] = UInt8((framebufferHeight >> 8) & 0xff)
        req[9] = UInt8(framebufferHeight & 0xff)
        sendRaw(req)
    }

    private func processBuffer() {
        while true {
            switch phase {
            case .version:
                guard buffer.count >= 12 else { return }
                consume(12)
                sendRaw(Data("RFB 003.008\n".utf8))
                phase = .securityCount

            case .securityCount:
                guard buffer.count >= 1 else { return }
                let count = Int(buffer[buffer.startIndex])
                consume(1)
                if count == 0 {
                    state = .error
                    errorMessage = "Desktop server rejected connection."
                    disconnect()
                    return
                }
                phase = .securityTypes(count)

            case .securityTypes(let count):
                guard buffer.count >= count else { return }
                let types = Array(buffer.prefix(count))
                consume(count)
                let chosen: UInt8 = types.contains(1) ? 1 : (types.first ?? 1)
                sendRaw(Data([chosen]))
                phase = .securityResult

            case .securityResult:
                guard buffer.count >= 4 else { return }
                let status = readUInt32(at: 0)
                consume(4)
                if status != 0 {
                    state = .error
                    errorMessage = "Desktop authentication failed."
                    disconnect()
                    return
                }
                sendRaw(Data([1])) // ClientInit shared=1
                phase = .serverInit

            case .serverInit:
                guard buffer.count >= 24 else { return }
                let w = max(1, Int(readUInt16(at: 0)))
                let h = max(1, Int(readUInt16(at: 2)))
                let nameLen = Int(readUInt32(at: 20))
                consume(24)
                resizeFramebuffer(width: w, height: h)
                phase = .serverName(nameLen)

            case .serverName(let nameLen):
                guard buffer.count >= nameLen else { return }
                consume(nameLen)
                phase = .running
                reconnectAttempt = 0
                state = .connected
                sendPixelFormatAndEncodings()
                requestFramebufferUpdate(incremental: false)

            case .running:
                if serverCutTextRemaining > 0 {
                    guard buffer.count >= serverCutTextRemaining else { return }
                    consume(serverCutTextRemaining)
                    serverCutTextRemaining = 0
                    continue
                }
                if pendingRects > 0 {
                    guard processRectangle() else { return }
                    pendingRects -= 1
                    if pendingRects == 0 {
                        refreshCGImage()
                        requestFramebufferUpdate(incremental: true)
                    }
                    continue
                }
                guard buffer.count >= 1 else { return }
                let msgType = buffer[buffer.startIndex]
                switch msgType {
                case 0: // FramebufferUpdate
                    guard buffer.count >= 4 else { return }
                    let rectCount = Int(readUInt16(at: 2))
                    consume(4)
                    pendingRects = rectCount
                    if rectCount == 0 {
                        requestFramebufferUpdate(incremental: true)
                    }
                case 1: // SetColourMapEntries
                    guard buffer.count >= 6 else { return }
                    let numColors = Int(readUInt16(at: 4))
                    let total = 6 + numColors * 6
                    guard buffer.count >= total else { return }
                    consume(total)
                case 2: // Bell
                    consume(1)
                case 3: // ServerCutText
                    guard buffer.count >= 8 else { return }
                    let len = Int(readUInt32(at: 4))
                    if len > Self.maxServerCutTextBytes {
                        failAndDisconnect("Desktop clipboard payload exceeded safe limit.")
                        return
                    }
                    consume(8)
                    serverCutTextRemaining = len
                default:
                    state = .error
                    errorMessage = "Unsupported desktop stream frame."
                    disconnect()
                    return
                }
            }
        }
    }

    private func processRectangle() -> Bool {
        guard buffer.count >= 12 else { return false }
        let x = Int(readUInt16(at: 0))
        let y = Int(readUInt16(at: 2))
        let w = Int(readUInt16(at: 4))
        let h = Int(readUInt16(at: 6))
        let encoding = Int32(bitPattern: readUInt32(at: 8))

        if encoding == -223 { // DesktopSize
            consume(12)
            resizeFramebuffer(width: max(1, w), height: max(1, h))
            return true
        }
        if encoding == 1 { // CopyRect
            guard buffer.count >= 16 else { return false }
            let srcX = Int(readUInt16(at: 12))
            let srcY = Int(readUInt16(at: 14))
            consume(16)
            copyRect(srcX: srcX, srcY: srcY, dstX: x, dstY: y, width: w, height: h)
            return true
        }
        if encoding == 0 { // Raw
            let byteLen = w * h * 4
            guard buffer.count >= 12 + byteLen else { return false }
            let rectBytes = buffer.subdata(in: (buffer.startIndex + 12)..<(buffer.startIndex + 12 + byteLen))
            consume(12 + byteLen)
            blitRaw(rectBytes, x: x, y: y, width: w, height: h)
            return true
        }
        state = .error
        errorMessage = "Unsupported desktop encoding."
        disconnect()
        return false
    }

    private func resizeFramebuffer(width: Int, height: Int) {
        guard width >= 1, height >= 1,
              width <= Self.maxFramebufferWidth,
              height <= Self.maxFramebufferHeight else {
            failAndDisconnect("Desktop dimensions exceeded supported display bounds.")
            return
        }
        framebufferWidth = width
        framebufferHeight = height
        framebuffer = Data(repeating: 0, count: width * height * 4)
        for i in stride(from: 3, to: framebuffer.count, by: 4) {
            framebuffer[i] = 255
        }
    }

    private func blitRaw(_ rect: Data, x: Int, y: Int, width: Int, height: Int) {
        guard width > 0, height > 0 else { return }
        framebuffer.withUnsafeMutableBytes { dstBuf in
            rect.withUnsafeBytes { srcBuf in
                guard let dst = dstBuf.bindMemory(to: UInt8.self).baseAddress,
                      let src = srcBuf.bindMemory(to: UInt8.self).baseAddress else { return }
                for row in 0..<height {
                    let dy = y + row
                    guard dy >= 0, dy < framebufferHeight else { continue }
                    for col in 0..<width {
                        let dx = x + col
                        guard dx >= 0, dx < framebufferWidth else { continue }
                        let si = (row * width + col) * 4
                        let di = (dy * framebufferWidth + dx) * 4
                        dst[di] = src[si]
                        dst[di + 1] = src[si + 1]
                        dst[di + 2] = src[si + 2]
                        dst[di + 3] = 255
                    }
                }
            }
        }
    }

    private func copyRect(srcX: Int, srcY: Int, dstX: Int, dstY: Int, width: Int, height: Int) {
        guard width > 0, height > 0 else { return }
        let snapshot = framebuffer
        framebuffer.withUnsafeMutableBytes { dstBuf in
            snapshot.withUnsafeBytes { srcBuf in
                guard let dst = dstBuf.bindMemory(to: UInt8.self).baseAddress,
                      let src = srcBuf.bindMemory(to: UInt8.self).baseAddress else { return }
                for row in 0..<height {
                    let sy = srcY + row
                    let dy = dstY + row
                    guard sy >= 0, sy < framebufferHeight, dy >= 0, dy < framebufferHeight else { continue }
                    for col in 0..<width {
                        let sx = srcX + col
                        let dx = dstX + col
                        guard sx >= 0, sx < framebufferWidth, dx >= 0, dx < framebufferWidth else { continue }
                        let si = (sy * framebufferWidth + sx) * 4
                        let di = (dy * framebufferWidth + dx) * 4
                        dst[di] = src[si]
                        dst[di + 1] = src[si + 1]
                        dst[di + 2] = src[si + 2]
                        dst[di + 3] = 255
                    }
                }
            }
        }
    }

    private func refreshCGImage() {
        let width = framebufferWidth
        let height = framebufferHeight
        guard width > 0, height > 0, framebuffer.count == width * height * 4 else { return }
        guard let provider = CGDataProvider(data: framebuffer as CFData) else { return }
        let colorSpace = CGColorSpaceCreateDeviceRGB()
        let bitmapInfo = CGBitmapInfo(rawValue: CGImageAlphaInfo.premultipliedLast.rawValue)
        if let image = CGImage(
            width: width,
            height: height,
            bitsPerComponent: 8,
            bitsPerPixel: 32,
            bytesPerRow: width * 4,
            space: colorSpace,
            bitmapInfo: bitmapInfo,
            provider: provider,
            decode: nil,
            shouldInterpolate: true,
            intent: .defaultIntent
        ) {
            currentImage = image
        }
    }

    private func consume(_ count: Int) {
        if count >= buffer.count {
            buffer.removeAll(keepingCapacity: true)
        } else {
            buffer.removeFirst(count)
        }
    }

    private func readUInt16(at offset: Int) -> UInt16 {
        let i = buffer.startIndex + offset
        return (UInt16(buffer[i]) << 8) | UInt16(buffer[i + 1])
    }

    private func readUInt32(at offset: Int) -> UInt32 {
        let i = buffer.startIndex + offset
        return (UInt32(buffer[i]) << 24)
            | (UInt32(buffer[i + 1]) << 16)
            | (UInt32(buffer[i + 2]) << 8)
            | UInt32(buffer[i + 3])
    }

    private func writeInt32(_ data: inout Data, offset: Int, value: Int32) {
        let u = UInt32(bitPattern: value)
        data[offset] = UInt8((u >> 24) & 0xff)
        data[offset + 1] = UInt8((u >> 16) & 0xff)
        data[offset + 2] = UInt8((u >> 8) & 0xff)
        data[offset + 3] = UInt8(u & 0xff)
    }
}
