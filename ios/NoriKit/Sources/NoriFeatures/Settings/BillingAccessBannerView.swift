import SwiftUI
import NoriCore
import NoriDesign

public struct BillingAccessBannerView<Trailing: View>: View {
    public let block: BillingAccessBlock
    public let allowBYOK: Bool
    private let trailing: Trailing

    public init(
        block: BillingAccessBlock,
        allowBYOK: Bool = true,
        @ViewBuilder trailing: () -> Trailing
    ) {
        self.block = block
        self.allowBYOK = allowBYOK
        self.trailing = trailing()
    }

    private var tone: NoriTone {
        block.isSuspended ? .danger : .warning
    }

    private var title: String {
        switch block.code {
        case .accountSuspended:
            return "Account suspended"
        case .subscriptionInactive:
            return "Subscription inactive"
        case .creditExhausted:
            return allowBYOK ? "Hosted credits used up" : "Trial credits used up"
        }
    }

    private var systemImage: String {
        switch block.code {
        case .accountSuspended:
            return "exclamationmark.octagon"
        case .subscriptionInactive:
            return "exclamationmark.circle"
        case .creditExhausted:
            return "creditcard"
        }
    }

    public var body: some View {
        NoriNotice(
            title: title,
            detail: block.message,
            tone: tone,
            systemImage: systemImage
        ) {
            trailing
        }
    }
}

public extension BillingAccessBannerView where Trailing == EmptyView {
    init(block: BillingAccessBlock, allowBYOK: Bool = true) {
        self.init(block: block, allowBYOK: allowBYOK, trailing: { EmptyView() })
    }
}
