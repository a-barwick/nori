import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

private struct DocumentPresentation: Identifiable, Hashable {
    let id: String
}

public struct NoriRootView: View {
    @AppStorage(NoriAppearance.storageKey) private var appearanceRawValue: String = NoriAppearance.system.rawValue
    @AppStorage(NoriTabBarPosition.storageKey) private var tabBarPositionRawValue: String = NoriTabBarPosition.top.rawValue
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var horizontalSizeClass
    #endif
    @Namespace private var tabBarSelectionNamespace
    @State private var store: NoriStore

    private static let topBarChromeHeight: CGFloat = 56

    private var appearance: NoriAppearance {
        NoriAppearance(storedRawValue: appearanceRawValue)
    }

    private var tabBarPosition: NoriTabBarPosition {
        NoriTabBarPosition(storedRawValue: tabBarPositionRawValue)
    }

    private var isPhoneDevice: Bool {
        #if os(iOS)
        UIDevice.current.userInterfaceIdiom == .phone
        #else
        false
        #endif
    }

    private var effectiveBarPosition: NoriTabBarPosition {
        isPhoneDevice ? tabBarPosition : .top
    }

    private var keepsPersistentTopBarOverDetails: Bool {
        #if os(iOS)
        !isPhoneDevice && horizontalSizeClass == .regular
        #else
        true
        #endif
    }

    private var isAtTabRoot: Bool {
        switch selectedTab {
        case .chat: return true
        case .work: return workPath.isEmpty
        case .more: return morePath.isEmpty
        }
    }

    private var usesPersistentTopBar: Bool {
        effectiveBarPosition == .top && keepsPersistentTopBarOverDetails
    }

    private var usesRootInlineTopBar: Bool {
        effectiveBarPosition == .top && !keepsPersistentTopBarOverDetails
    }

    private var shouldPadToastForTopBar: Bool {
        usesPersistentTopBar || (usesRootInlineTopBar && isAtTabRoot)
    }

    private var shouldShowBottomBar: Bool {
        effectiveBarPosition == .bottom && !isKeyboardVisible
    }

    private var hasWorkAttention: Bool {
        store.actionableNeedsYouCount > 0
    }

    private var hasSettingsAttention: Bool {
        store.appsAttentionCount > 0 || store.needsAnyKeySetup
    }

    private var settingsAttentionAccessibilityValue: String {
        if store.needsModelKeySetup && store.needsExaKeySetup {
            return "AI model key and web search key needed"
        }
        if store.needsModelKeySetup {
            return "AI model key needed"
        }
        if store.needsExaKeySetup {
            return "Web search key needed"
        }
        return store.appsAttentionCount > 0 ? "Requires attention" : ""
    }

    private var activePillFill: Color {
        colorScheme == .dark ? NoriColors.selected : NoriColors.surface
    }

    private var activePillShadow: Color {
        colorScheme == .dark ? .clear : Color(hex: 0x190936, alpha: 0.08)
    }

    private var glassBorderColor: Color {
        colorScheme == .dark ? Color.white.opacity(0.11) : Color(hex: 0x190936, alpha: 0.09)
    }

    private var glassShadowColor: Color {
        colorScheme == .dark ? Color.black.opacity(0.45) : Color(hex: 0x190936, alpha: 0.09)
    }

    @State private var selectedTab: NoriTab = .chat
    @State private var chatScrollToBottomTrigger: Int = 0
    @State private var workPath: [WorkRoute] = []
    @State private var didApplyInitialWorkTab = false
    @State private var morePath: [MoreRoute] = []
    @State private var isKeyboardVisible = false

    @State private var presentedDocument: DocumentPresentation?
    @State private var hasBackgrounded = false
    @State private var tabRefreshTask: Task<Void, Never>?
    @State private var resumeTask: Task<Void, Never>?

    public init(store: NoriStore) {
        self._store = State(initialValue: store)
    }

    public init() {
        self._store = State(initialValue: NoriStore())
    }

    public var body: some View {
        Group {
            if store.isResettingData {
                DataResetLoadingView()
            } else if store.authState.isSignedIn {
                if store.accountDeletePending {
                    AccountDeleteRecoveryView(store: store)
                } else if store.dataResetRecoveryRequired {
                    DataResetRecoveryView(store: store)
                } else if store.dataResetStatusRequired {
                    DataResetStatusView(store: store)
                } else if store.shouldShowOnboarding {
                    OnboardingView(store: store)
                } else {
                    signedInBody
                }
            } else if store.isBootstrappingAuth {
                bootstrapLoadingView
            } else {
                SignInView(store: store)
            }
        }
        .task {
            await store.bootstrapIfNeeded()
            if let pending = store.pendingDeepLink {
                applyDeepLink(pending)
                store.pendingDeepLink = nil
            }
        }
        .onOpenURL { url in
            store.handleIncomingURL(url)
        }
        .onChange(of: store.pendingDeepLink) { _, next in
            guard let next else { return }
            applyDeepLink(next)
            store.pendingDeepLink = nil
        }
        .onChange(of: scenePhase) { _, newPhase in
            switch newPhase {
            case .background:
                hasBackgrounded = true
                tabRefreshTask?.cancel()
                tabRefreshTask = nil
                resumeTask?.cancel()
                resumeTask = nil
                store.pauseForBackground()
            case .active:
                guard hasBackgrounded else { return }
                hasBackgrounded = false
                guard store.authState.isSignedIn else { return }
                resumeTask?.cancel()
                resumeTask = Task {
                    await store.resumeFromBackground()
                }
            case .inactive:
                break
            @unknown default:
                break
            }
        }
        .onChange(of: store.authState.isSignedIn) { _, isSignedIn in
            if !isSignedIn {
                tabRefreshTask?.cancel()
                tabRefreshTask = nil
                resumeTask?.cancel()
                resumeTask = nil
                didApplyInitialWorkTab = false
            } else if selectedTab != .chat {
                scheduleRefresh(for: selectedTab)
            }
        }
        .onChange(of: store.shouldShowOnboarding) { _, showingOnboarding in
            if showingOnboarding {
                clearNavigation()
            }
        }
        .onChange(of: store.dataResetRecoveryRequired) { _, required in
            if required { clearNavigation() }
        }
        .onChange(of: selectedTab) { _, nextTab in
            scheduleRefresh(for: nextTab)
        }
        .preferredColorScheme(appearance.colorScheme)
    }

    private var bootstrapLoadingView: some View {
        VStack {
            Spacer()
            NoriMark(size: 56, state: .idle)
            Spacer()
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(NoriColors.canvas.ignoresSafeArea())
    }

    private func clearNavigation() {
        tabRefreshTask?.cancel()
        tabRefreshTask = nil
        selectedTab = .chat
        workPath = []
        morePath = []
        didApplyInitialWorkTab = false
        presentedDocument = nil
    }

    private func scheduleRefresh(for tab: NoriTab) {
        tabRefreshTask?.cancel()
        guard store.authState.isSignedIn else { return }
        tabRefreshTask = Task { await refresh(for: tab) }
    }

    private func refresh(for tab: NoriTab) async {
        await store.refreshForTab(tab, force: false)
        if tab == .work, !Task.isCancelled, !didApplyInitialWorkTab {
            didApplyInitialWorkTab = true
        }
    }

    private func selectTab(_ next: NoriTab) {
        if next == selectedTab {
            switch next {
            case .chat:
                chatScrollToBottomTrigger &+= 1
            case .work:
                if !workPath.isEmpty { workPath = [] }
            case .more:
                if !morePath.isEmpty { morePath = [] }
            }
            return
        }
        #if canImport(UIKit)
        if selectedTab == .chat && next != .chat {
            UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
        }
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
        #endif
        if reduceMotion {
            selectedTab = next
        } else {
            withAnimation(.spring(response: 0.24, dampingFraction: 0.88)) {
                selectedTab = next
            }
        }
    }

    @ViewBuilder
    private var signedInBody: some View {
        ZStack(alignment: .top) {
            ZStack {
                ChatView(
                    store: store,
                    scrollToBottomTrigger: chatScrollToBottomTrigger,
                    onOpenActivity: jumpToNeedsYou,
                    onOpenDocument: openDocument,
                    onNavigateToNeedsYou: jumpToNeedsYou,
                    onOpenTask: openWorkTask,
                    onOpenModels: openModels,
                    onOpenUsage: openUsage
                )
                .safeAreaInset(edge: .top, spacing: 0) {
                    if usesRootInlineTopBar {
                        topNavigationBar
                    }
                }
                .opacity(selectedTab == .chat ? 1 : 0)
                .allowsHitTesting(selectedTab == .chat)
                .accessibilityHidden(selectedTab != .chat)

                NavigationStack(path: $workPath) {
                    WorkView(
                        store: store,
                        isLoadingOverview: !didApplyInitialWorkTab,
                        onOpenDocument: openDocument,
                        onOpenTask: openWorkTask,
                        onStartInChat: startInChat,
                        onOpenModels: openModels
                    )
                    .safeAreaInset(edge: .top, spacing: 0) {
                        if usesRootInlineTopBar {
                            topNavigationBar
                        }
                    }
                    .navigationDestination(for: WorkRoute.self) { route in
                        switch route {
                        case .actionNeeded:
                            WorkActionNeededView(
                                store: store,
                                onOpenTask: openWorkTask
                            )
                        case .tasks:
                            WorkTasksView(
                                store: store,
                                isLoadingOverview: !didApplyInitialWorkTab,
                                onOpenTask: openWorkTask,
                                onStartInChat: startInChat
                            )
                        case .automations:
                            RoutinesView(store: store)
                        case .artifacts:
                            LibraryView(store: store, onOpenDocument: openDocument)
                        case .computer:
                            ComputerView(store: store)
                        case .taskDetail(let taskID):
                            WorkDetailView(
                                store: store,
                                taskID: taskID,
                                onOpenDocument: openDocument,
                                onStartInChat: startInChat,
                                onOpenModels: openModels
                            )
                        }
                    }
                }
                .opacity(selectedTab == .work ? 1 : 0)
                .allowsHitTesting(selectedTab == .work)
                .accessibilityHidden(selectedTab != .work)

                NavigationStack(path: $morePath) {
                    MoreHubView(store: store)
                        .safeAreaInset(edge: .top, spacing: 0) {
                            if usesRootInlineTopBar {
                                topNavigationBar
                            }
                        }
                        .navigationDestination(for: MoreRoute.self) { route in
                            switch route {
                            case .apps:
                                AppsView(store: store)
                            case .mcpDetail(let serverID):
                                MCPDetailView(store: store, serverID: serverID)
                            case .personalization:
                                PersonalizationSettingsView(store: store)
                            case .memory:
                                MemoryView(store: store)
                            case .permissions:
                                StandingPermissionsView(store: store)
                            case .notifications:
                                NotificationsSettingsView(store: store)
                            case .preferences:
                                PreferencesBudgetsView(store: store)
                            case .apiCosts:
                                APICostsView(store: store)
                            case .models:
                                ModelsSettingsView(store: store)
                            case .appearance:
                                AppearanceSettingsView()
                            case .account:
                                AccountSettingsView(store: store)
                            case .history:
                                ActionHistoryView(store: store)
                            case .diagnostics:
                                DiagnosticsSettingsView(store: store)
                            }
                        }
                }
                .opacity(selectedTab == .more ? 1 : 0)
                .allowsHitTesting(selectedTab == .more)
                .accessibilityHidden(selectedTab != .more)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .tint(NoriColors.accent)
            .background(NoriColors.canvas.ignoresSafeArea())
            .safeAreaInset(edge: .top, spacing: 0) {
                if usesPersistentTopBar {
                    topNavigationBar
                }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if shouldShowBottomBar {
                    bottomNavigationBar
                }
            }

            if let toast = store.toastMessage {
                NoriToast(toast)
                    .padding(.top, shouldPadToastForTopBar ? Self.topBarChromeHeight + 6 : 10)
                    .padding(.horizontal, 16)
                    .transition(.move(edge: .top).combined(with: .opacity))
                    .zIndex(100)
                    .allowsHitTesting(false)
            }
        }
        .animation(.spring(response: 0.28, dampingFraction: 0.85), value: store.toastMessage)
        .animation(reduceMotion ? nil : .easeOut(duration: 0.18), value: isKeyboardVisible)
        #if canImport(UIKit)
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillChangeFrameNotification)) { notification in
            guard let endFrame = notification.userInfo?[UIResponder.keyboardFrameEndUserInfoKey] as? CGRect else {
                return
            }
            let screenHeight = UIScreen.main.bounds.height
            let overlapsScreen = endFrame.minY < screenHeight - 10
            isKeyboardVisible = overlapsScreen && endFrame.height > 100
        }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillHideNotification)) { _ in
            isKeyboardVisible = false
        }
        #endif
        .sheet(item: $presentedDocument) { doc in
            DocumentReaderSheet(store: store, itemID: doc.id)
                .presentationDetents([.large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
    }

    private var topNavigationBar: some View {
        ZStack {
            HStack(spacing: 2) {
                topPillSegment(tab: .chat, label: "Chat")
                topPillSegment(tab: .work, label: "Work")
            }
            .padding(4)
            .frame(height: 42)
            .background { glassChromeBackground(Capsule()) }

            HStack {
                Spacer()
                settingsCogButton(diameter: 42, iconSize: 18, dotInset: 8)
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 6)
        .padding(.bottom, 8)
        .background(
            LinearGradient(
                colors: [
                    NoriColors.canvas.opacity(0.96),
                    NoriColors.canvas.opacity(0.0)
                ],
                startPoint: .top,
                endPoint: .bottom
            )
            .ignoresSafeArea(edges: .top)
        )
    }

    private func topPillSegment(tab: NoriTab, label: String) -> some View {
        let isSelected = selectedTab == tab
        return Button {
            selectTab(tab)
        } label: {
            HStack(spacing: 6) {
                if tab == .chat {
                    NoriMark.tabBarIcon(selected: isSelected)
                        .renderingMode(.template)
                        .resizable()
                        .scaledToFit()
                        .frame(width: 17, height: 17)
                } else {
                    Image(systemName: "checklist")
                        .font(.system(size: 14, weight: .semibold))
                }
                Text(label)
                    .font(.system(size: 13.5, weight: .semibold))
                if tab == .work && hasWorkAttention {
                    Circle()
                        .fill(NoriColors.brandPrimary)
                        .frame(width: 6.5, height: 6.5)
                        .accessibilityHidden(true)
                }
            }
            .foregroundStyle(isSelected ? NoriColors.accent : NoriColors.ink2)
            .padding(.horizontal, 14)
            .frame(height: 34)
            .background {
                if isSelected {
                    Capsule()
                        .fill(activePillFill)
                        .shadow(color: activePillShadow, radius: 3, x: 0, y: 1)
                        .matchedGeometryEffect(id: "noriTopTabSelection", in: tabBarSelectionNamespace)
                }
            }
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityValue(tab == .work && hasWorkAttention ? "Action needed" : "")
        .accessibilityAddTraits(isSelected ? .isSelected : [])
        #if os(iOS)
        .accessibilityShowsLargeContentViewer()
        #endif
    }

    private var bottomNavigationBar: some View {
        HStack(spacing: 10) {
            HStack(spacing: 4) {
                bottomPillSegment(tab: .chat, label: "Chat")
                bottomPillSegment(tab: .work, label: "Work")
            }
            .padding(.horizontal, 6)
            .padding(.vertical, 5)
            .frame(height: 56)
            .background { glassChromeBackground(Capsule()) }

            settingsCogButton(diameter: 56, iconSize: 21, dotInset: 13)
        }
        .padding(.horizontal, 18)
        .padding(.top, 6)
        .padding(.bottom, 8)
    }

    private func bottomPillSegment(tab: NoriTab, label: String) -> some View {
        let isSelected = selectedTab == tab
        return Button {
            selectTab(tab)
        } label: {
            VStack(spacing: 2) {
                ZStack(alignment: .topTrailing) {
                    if tab == .chat {
                        NoriMark.tabBarIcon(selected: isSelected)
                            .renderingMode(.template)
                            .resizable()
                            .scaledToFit()
                            .frame(width: 21, height: 21)
                    } else {
                        Image(systemName: "checklist")
                            .font(.system(size: 18, weight: .semibold))
                    }
                    if tab == .work && hasWorkAttention {
                        Circle()
                            .fill(NoriColors.brandPrimary)
                            .frame(width: 6.5, height: 6.5)
                            .offset(x: 6, y: -2)
                            .accessibilityHidden(true)
                    }
                }
                Text(label)
                    .font(.system(size: 10.5, weight: .semibold))
            }
            .foregroundStyle(isSelected ? NoriColors.accent : NoriColors.ink2)
            .frame(minWidth: 92)
            .frame(height: 46)
            .background {
                if isSelected {
                    Capsule()
                        .fill(activePillFill)
                        .shadow(color: activePillShadow, radius: 3, x: 0, y: 1)
                        .matchedGeometryEffect(id: "noriBottomTabSelection", in: tabBarSelectionNamespace)
                }
            }
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityValue(tab == .work && hasWorkAttention ? "Action needed" : "")
        .accessibilityAddTraits(isSelected ? .isSelected : [])
        #if os(iOS)
        .accessibilityShowsLargeContentViewer()
        #endif
    }

    private func settingsCogButton(diameter: CGFloat, iconSize: CGFloat, dotInset: CGFloat) -> some View {
        let isSelected = selectedTab == .more
        return Button {
            selectTab(.more)
        } label: {
            ZStack {
                Image(systemName: "gearshape")
                    .font(.system(size: iconSize, weight: .semibold))
                    .foregroundStyle(isSelected ? NoriColors.accent : NoriColors.ink2)
            }
            .frame(width: diameter, height: diameter)
            .background {
                if isSelected {
                    Circle()
                        .fill(activePillFill)
                        .overlay(Circle().strokeBorder(glassBorderColor, lineWidth: 1))
                        .shadow(color: glassShadowColor, radius: 12, x: 0, y: 4)
                } else {
                    glassChromeBackground(Circle())
                }
            }
            .overlay(alignment: .topTrailing) {
                if hasSettingsAttention {
                    Circle()
                        .fill(store.needsAnyKeySetup ? NoriColors.danger : NoriColors.brandPrimary)
                        .frame(width: 7, height: 7)
                        .overlay(Circle().stroke(NoriColors.canvas, lineWidth: 1.5))
                        .padding(.top, dotInset)
                        .padding(.trailing, dotInset)
                        .accessibilityHidden(true)
                }
            }
            .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Settings")
        .accessibilityValue(settingsAttentionAccessibilityValue)
        .accessibilityAddTraits(isSelected ? .isSelected : [])
        #if os(iOS)
        .accessibilityShowsLargeContentViewer()
        #endif
    }

    @ViewBuilder
    private func glassChromeBackground<S: InsettableShape>(_ shape: S) -> some View {
        shape
            .fill(.ultraThinMaterial)
            .overlay(shape.fill(NoriColors.glass.opacity(0.45)))
            .overlay(shape.strokeBorder(glassBorderColor, lineWidth: 1))
            .shadow(color: glassShadowColor, radius: 14, x: 0, y: 5)
    }


    private func openDocument(_ item: NoriCore.LibraryItem) {
        presentedDocument = DocumentPresentation(id: item.id)
    }

    private func jumpToNeedsYou() {
        presentedDocument = nil
        workPath = []
        selectedTab = .work
    }

    private func openModels() {
        presentedDocument = nil
        selectedTab = .more
        morePath = [.models]
    }

    private func openUsage() {
        presentedDocument = nil
        selectedTab = .more
        morePath = [.apiCosts]
    }

    private func openWorkTask(_ taskID: String) {
        presentedDocument = nil
        selectedTab = .work
        workPath = [.taskDetail(taskID)]
    }

    private func startInChat(draft: String) {
        presentedDocument = nil
        store.pendingComposerDraft = draft
        selectedTab = .chat
    }

    private func applyDeepLink(_ link: NoriDeepLink) {
        if link != .onboarding {
            store.isShowingOnboardingReplay = false
        }
        switch link {
        case .tab(let tab):
            presentedDocument = nil
            selectedTab = tab
            if tab == .work {
                workPath = []
            } else if tab == .more {
                morePath = []
            }
        case .apps:
            presentedDocument = nil
            selectedTab = .more
            morePath = [.apps]
        case .memory:
            presentedDocument = nil
            selectedTab = .more
            morePath = [.memory]
        case .library:
            presentedDocument = nil
            selectedTab = .work
            workPath = [.artifacts]
        case .document(let itemID):
            presentedDocument = DocumentPresentation(id: itemID)
        case .routines:
            presentedDocument = nil
            selectedTab = .work
            workPath = [.automations]
        case .settings:
            presentedDocument = nil
            selectedTab = .more
            morePath = []
        case .onboarding:
            presentedDocument = nil
            store.isShowingOnboardingReplay = true
        case .workDetail(let taskID):
            openWorkTask(taskID)
        case .reviewInteraction(let interactionID):
            presentedDocument = nil
            selectedTab = .chat
            store.pendingReviewInteractionID = interactionID
        case .activitySheet:
            jumpToNeedsYou()
        case .oauthCallback:
            break
        }
    }
}
