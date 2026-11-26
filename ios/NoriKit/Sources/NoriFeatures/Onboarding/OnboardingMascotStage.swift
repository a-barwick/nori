import SwiftUI
import NoriCore
import NoriDesign

enum OnboardingMood: Equatable, Sendable {
    case hello
    case hop
    case nod
    case shake
    case celebrate
}

struct OnboardingMoodRequest: Equatable, Sendable {
    let id = UUID()
    let mood: OnboardingMood
    let happy: Bool
    let sparkles: Bool
    let duration: Double
}

struct OnboardingMascotStage: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    let step: Int
    let markState: MarkState
    let moodRequest: OnboardingMoodRequest?
    let onTap: () -> Void

    @State private var markOffsetY: CGFloat = 0
    @State private var markOffsetX: CGFloat = 0
    @State private var markRotation: Double = 0
    @State private var markScale: CGFloat = 1
    @State private var isHappy = false
    @State private var showSparkles = false

    var body: some View {
        Button(action: onTap) {
            ZStack {
                if showSparkles && !reduceMotion {
                    OnboardingSparkles()
                        .transition(.opacity)
                }
                NoriMark(
                    size: step == 1 ? 76 : 72,
                    state: markState,
                    happy: isHappy
                )
                .scaleEffect(markScale)
                .rotationEffect(.degrees(markRotation))
                .offset(x: markOffsetX, y: markOffsetY)
            }
            .frame(height: 86)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Nori")
        .padding(.bottom, 12)
        .task(id: moodRequest) {
            guard let request = moodRequest else { return }
            await performMood(request)
        }
    }

    private func performMood(_ request: OnboardingMoodRequest) async {
        let allowMotion = !reduceMotion
        async let happyTask: Void = runHappy(enabled: request.happy, duration: request.duration)
        async let sparklesTask: Void = runSparkles(enabled: request.sparkles && allowMotion)
        async let motionTask: Void = runMotion(enabled: allowMotion, mood: request.mood)
        _ = await (happyTask, sparklesTask, motionTask)
    }

    private func runHappy(enabled: Bool, duration: Double) async {
        guard enabled else { return }
        isHappy = true
        do {
            try await Task.sleep(for: .seconds(duration))
        } catch {
            return
        }
        guard !Task.isCancelled else { return }
        isHappy = false
    }

    private func runSparkles(enabled: Bool) async {
        guard enabled else { return }
        showSparkles = false
        withAnimation(.easeOut(duration: 0.2)) {
            showSparkles = true
        }
        do {
            try await Task.sleep(for: .milliseconds(720))
        } catch {
            return
        }
        guard !Task.isCancelled else { return }
        withAnimation(.easeOut(duration: 0.15)) {
            showSparkles = false
        }
    }

    private func runMotion(enabled: Bool, mood: OnboardingMood) async {
        guard enabled else { return }
        await runMotionSequence(mood)
    }

    private func runMotionSequence(_ mood: OnboardingMood) async {
        do {
            switch mood {
            case .hello:
                withAnimation(.spring(response: 0.34, dampingFraction: 0.62)) {
                    markOffsetY = -6
                    markRotation = -6
                    markScale = 1.03
                }
                try await Task.sleep(for: .milliseconds(260))
                withAnimation(.spring(response: 0.32, dampingFraction: 0.68)) {
                    markOffsetY = 0
                    markRotation = 4
                    markScale = 1.0
                }
                try await Task.sleep(for: .milliseconds(220))
                withAnimation(.spring(response: 0.28, dampingFraction: 0.8)) {
                    markRotation = 0
                }
            case .hop:
                withAnimation(.spring(response: 0.24, dampingFraction: 0.6)) {
                    markOffsetY = -9
                    markRotation = -3
                    markScale = 1.04
                }
                try await Task.sleep(for: .milliseconds(210))
                withAnimation(.spring(response: 0.28, dampingFraction: 0.72)) {
                    markOffsetY = 0
                    markRotation = 0
                    markScale = 1.0
                }
            case .nod:
                withAnimation(.easeOut(duration: 0.15)) {
                    markOffsetY = 3
                    markRotation = 3
                }
                try await Task.sleep(for: .milliseconds(150))
                withAnimation(.spring(response: 0.26, dampingFraction: 0.78)) {
                    markOffsetY = 0
                    markRotation = 0
                }
            case .shake:
                for x in [-6.0, 6.0, -4.0, 0.0] {
                    withAnimation(.easeInOut(duration: 0.08)) {
                        markOffsetX = x
                        markRotation = x * 0.6
                    }
                    try await Task.sleep(for: .milliseconds(85))
                }
            case .celebrate:
                withAnimation(.spring(response: 0.26, dampingFraction: 0.55)) {
                    markOffsetY = -12
                    markRotation = -5
                    markScale = 1.06
                }
                try await Task.sleep(for: .milliseconds(250))
                withAnimation(.spring(response: 0.3, dampingFraction: 0.7)) {
                    markOffsetY = 0
                    markRotation = 0
                    markScale = 1.0
                }
            }
        } catch {
            markOffsetX = 0
            markOffsetY = 0
            markRotation = 0
            markScale = 1.0
        }
    }
}

private struct OnboardingSparkles: View {
    @State private var expanded = false

    var body: some View {
        ZStack {
            Circle()
                .fill(NoriColors.accent)
                .frame(width: 6, height: 6)
                .offset(x: expanded ? -46 : -24, y: expanded ? -28 : -14)
                .opacity(expanded ? 0 : 1)
            Circle()
                .fill(NoriTone.ok.foreground)
                .frame(width: 6, height: 6)
                .offset(x: expanded ? 46 : 24, y: expanded ? -26 : -12)
                .opacity(expanded ? 0 : 1)
            Circle()
                .fill(NoriColors.brandPrimary)
                .frame(width: 6, height: 6)
                .offset(x: expanded ? -48 : -26, y: expanded ? 12 : 6)
                .opacity(expanded ? 0 : 1)
            Circle()
                .fill(NoriColors.accent)
                .frame(width: 6, height: 6)
                .offset(x: expanded ? 48 : 26, y: expanded ? 14 : 6)
                .opacity(expanded ? 0 : 1)
        }
        .allowsHitTesting(false)
        .onAppear {
            withAnimation(.easeOut(duration: 0.65)) {
                expanded = true
            }
        }
    }
}

struct OnboardingChipFlow: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let maxWidth = proposal.width ?? .infinity
        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0

        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x + size.width > maxWidth, x > 0 {
                x = 0
                y += rowHeight + spacing
                rowHeight = 0
            }
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }
        return CGSize(width: maxWidth.isFinite ? maxWidth : x, height: y + rowHeight)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX
        var y = bounds.minY
        var rowHeight: CGFloat = 0

        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x + size.width > bounds.maxX, x > bounds.minX {
                x = bounds.minX
                y += rowHeight + spacing
                rowHeight = 0
            }
            subview.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }
    }
}
