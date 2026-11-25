import SwiftUI
import NoriCore

/// Exact onigiri silhouette path ported from `web/src/components/mark/NoriMark.tsx` (96×96 coordinate space).
public struct OnigiriRiceShape: Shape {
    public init() {}

    public func path(in rect: CGRect) -> Path {
        let sx = rect.width / 96.0
        let sy = rect.height / 96.0
        func pt(_ x: CGFloat, _ y: CGFloat) -> CGPoint {
            CGPoint(x: rect.minX + x * sx, y: rect.minY + y * sy)
        }

        // M48 11
        // C54.5 11 59.2 14.3 62.6 20
        // L84.6 58.4
        // C91.5 70.5 84.6 85 70.6 85
        // H25.4
        // C11.4 85 4.5 70.5 11.4 58.4
        // L33.4 20
        // C36.8 14.3 41.5 11 48 11 Z
        var p = Path()
        p.move(to: pt(48, 11))
        p.addCurve(to: pt(62.6, 20), control1: pt(54.5, 11), control2: pt(59.2, 14.3))
        p.addLine(to: pt(84.6, 58.4))
        p.addCurve(to: pt(70.6, 85), control1: pt(91.5, 70.5), control2: pt(84.6, 85))
        p.addLine(to: pt(25.4, 85))
        p.addCurve(to: pt(11.4, 58.4), control1: pt(11.4, 85), control2: pt(4.5, 70.5))
        p.addLine(to: pt(33.4, 20))
        p.addCurve(to: pt(48, 11), control1: pt(36.8, 14.3), control2: pt(41.5, 11))
        p.closeSubpath()
        return p
    }
}

/// Animated Nori onigiri mark with appearance-aware palette:
/// - Light mode: `#FDFCFF` soft white rice, `#101A30` navy seaweed wrap, `#190936` outline & expression
/// - Dark mode: `#C6C4CE` soft light-gray body, `#171522` dark face & seaweed wrap, `#D1CFD8` subtle outer edge
/// - Periodic eye blink (idle / attentive), downward glance (attentive), and 2s thinking eye sweep (thinking).
public struct NoriMark: View {
    public let size: CGFloat
    public let state: MarkState

    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var isBlinking = false

    public init(size: CGFloat = 32, state: MarkState = .idle) {
        self.size = size
        self.state = state
    }

    public var body: some View {
        Group {
            if state == .thinking && !reduceMotion {
                TimelineView(.animation(minimumInterval: 1.0 / 30.0)) { context in
                    let offset = thinkingOffset(at: context.date.timeIntervalSinceReferenceDate)
                    markCanvas(eyeOffset: offset, blinkScaleY: 1.0)
                }
            } else {
                let lookY: CGFloat = (state == .attentive && !reduceMotion) ? 2.6 : 0.0
                markCanvas(
                    eyeOffset: CGSize(width: 0, height: lookY),
                    blinkScaleY: (isBlinking && !reduceMotion) ? 0.12 : 1.0
                )
                .animation(.easeOut(duration: 0.24), value: state)
                .animation(.easeInOut(duration: 0.09), value: isBlinking)
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
        .task(id: "\(state.rawValue)-\(reduceMotion)") {
            isBlinking = false
            guard !reduceMotion, state == .idle || state == .attentive else { return }
            while !Task.isCancelled {
                let waitMs = Int.random(in: 5200...8400)
                try? await Task.sleep(for: .milliseconds(waitMs))
                guard !Task.isCancelled else { break }
                isBlinking = true
                try? await Task.sleep(for: .milliseconds(140))
                guard !Task.isCancelled else { break }
                isBlinking = false
            }
        }
    }

    private func markCanvas(eyeOffset: CGSize, blinkScaleY: CGFloat) -> some View {
        let isDark = colorScheme == .dark
        let bodyColor = isDark ? Color(hex: 0xC6C4CE) : Color(hex: 0xFDFCFF)
        let seaweedColor = isDark ? Color(hex: 0x171522) : Color(hex: 0x101A30)
        let faceColor = isDark ? Color(hex: 0x171522) : Color(hex: 0x190936)
        let edgeColor = isDark ? Color(hex: 0xD1CFD8) : Color(hex: 0x190936)

        return Canvas { context, canvasSize in
            let s = min(canvasSize.width, canvasSize.height) / 96.0
            let ox = (canvasSize.width - 96.0 * s) / 2.0
            let oy = (canvasSize.height - 96.0 * s) / 2.0
            let box = CGRect(x: ox, y: oy, width: 96.0 * s, height: 96.0 * s)

            let ricePath = OnigiriRiceShape().path(in: box)

            // 1. Appearance-aware rice body fill (soft white in light, #C6C4CE light-gray in dark)
            context.fill(ricePath, with: .color(bodyColor))

            // 2. Seaweed wrap clipped cleanly to rice silhouette (x:30, y:61, w:36, h:30, rx:3)
            context.drawLayer { layer in
                layer.clip(to: ricePath)
                let wrapRect = CGRect(x: ox + 30.0 * s, y: oy + 61.0 * s, width: 36.0 * s, height: 30.0 * s)
                let wrapPath = Path(roundedRect: wrapRect, cornerRadius: 3.0 * s)
                layer.fill(wrapPath, with: .color(seaweedColor))
            }

            // 3. Outer silhouette stroke (dark ink in light, subtle #D1CFD8 edge in dark)
            let edgeWidth = isDark ? max(0.85, 1.35 * s) : max(1.0, 1.75 * s)
            context.stroke(
                ricePath,
                with: .color(edgeColor),
                style: StrokeStyle(lineWidth: edgeWidth, lineCap: .round, lineJoin: .round)
            )

            // 4. Eyes at (38.5, 46) and (57.5, 46), rx: 3.4, ry: 4.0
            let rx = 3.4 * s
            let ry = 4.0 * s * blinkScaleY
            let dx = eyeOffset.width * s
            let dy = eyeOffset.height * s

            for eyeCenterX in [38.5, 57.5] as [CGFloat] {
                let cx = ox + eyeCenterX * s + dx
                let cy = oy + 46.0 * s + dy
                let eyeRect = CGRect(x: cx - rx, y: cy - ry, width: rx * 2, height: ry * 2)
                context.fill(Path(ellipseIn: eyeRect), with: .color(faceColor))
            }

            // 5. Smile: M44 53.5 Q48 57.1 52 53.5
            var mouth = Path()
            mouth.move(to: CGPoint(x: ox + 44.0 * s, y: oy + 53.5 * s))
            mouth.addQuadCurve(
                to: CGPoint(x: ox + 52.0 * s, y: oy + 53.5 * s),
                control: CGPoint(x: ox + 48.0 * s, y: oy + 57.1 * s)
            )
            context.stroke(
                mouth,
                with: .color(faceColor),
                style: StrokeStyle(lineWidth: max(1.1, 2.6 * s), lineCap: .round, lineJoin: .round)
            )
        }
    }

    /// Replicates `@keyframes mark-think` (2s cycle: 0% -> 30% (-2.4,-2.6) -> 65% (2.4,-2.6) -> 100% (0,0)).
    private func thinkingOffset(at timestamp: TimeInterval) -> CGSize {
        let phase = (timestamp.truncatingRemainder(dividingBy: 2.0) + 2.0).truncatingRemainder(dividingBy: 2.0) / 2.0
        func ease(_ t: Double) -> Double {
            t * t * (3.0 - 2.0 * t)
        }
        if phase < 0.30 {
            let t = ease(phase / 0.30)
            return CGSize(width: -2.4 * t, height: -2.6 * t)
        } else if phase < 0.65 {
            let t = ease((phase - 0.30) / 0.35)
            return CGSize(width: -2.4 + 4.8 * t, height: -2.6)
        } else {
            let t = ease((phase - 0.65) / 0.35)
            return CGSize(width: 2.4 * (1.0 - t), height: -2.6 * (1.0 - t))
        }
    }
}

public struct NoriMedallion: View {
    public let size: CGFloat
    public let state: MarkState

    public init(size: CGFloat = 40, state: MarkState = .idle) {
        self.size = size
        self.state = state
    }

    public var body: some View {
        ZStack {
            NoriMark(size: round(size * 0.9), state: state)
        }
        .frame(width: size, height: size)
    }
}
