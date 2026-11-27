import SwiftUI
import NoriCore
import NoriDesign

private struct DocumentPresentation: Identifiable, Hashable {
    let id: String
}

public struct NoriRootView: View {
    @AppStorage(NoriAppearance.storageKey) private var appearanceRawValue: String = NoriAppearance.system.rawValue
    @Environment(\.scenePhase) private var scenePhase
    @State private var store: NoriStore

    private var appearance: NoriAppearance {
        NoriAppearance(storedRawValue: appearanceRawValue)
    }

    @State private var selectedTab: NoriTab = .chat
    @State private var chatScrollToBottomTrigger: Int = 0
    @State private var workPath: [WorkRoute] = []
    @State private var didApplyInitialWorkTab = false
    @State private var morePath: [MoreRoute] = []

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

    private var tabSelectionBinding: Binding<NoriTab> {
        Binding(
            get: { selectedTab },
            set: { next in
                if next == selectedTab && next == .chat {
                    chatScrollToBottomTrigger &+= 1
                } else {
                    selectedTab = next
                }
            }
        )
    }

    @ViewBuilder
    private var signedInBody: some View {
        ZStack(alignment: .top) {
            TabView(selection: tabSelectionBinding) {
                ChatView(
                    store: store,
                    scrollToBottomTrigger: chatScrollToBottomTrigger,
                    onOpenActivity: jumpToNeedsYou,
                    onOpenDocument: openDocument,
                    onNavigateToNeedsYou: jumpToNeedsYou,
                    onOpenTask: openWorkTask,
                    onOpenModels: openModels
                )
                .tabItem {
                    NoriMark.tabBarIcon(selected: selectedTab == .chat)
                    Text("Chat")
                }
                .tag(NoriTab.chat)

                NavigationStack(path: $workPath) {
                    WorkView(
                        store: store,
                        isLoadingOverview: !didApplyInitialWorkTab,
                        onOpenDocument: openDocument,
                        onOpenTask: openWorkTask,
                        onStartInChat: startInChat,
                        onOpenModels: openModels
                    )
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
                .tabItem {
                    Label("Work", systemImage: "checklist")
                }
                .badge(store.actionableNeedsYouCount)
                .tag(NoriTab.work)

                NavigationStack(path: $morePath) {
                    MoreHubView(store: store)
                        .navigationDestination(for: MoreRoute.self) { route in
                            switch route {
                            case .apps:
                                AppsView(store: store)
                            case .mcpDetail(let serverID):
                                MCPDetailView(store: store, serverID: serverID)
                            case .memory:
                                MemoryView(store: store)
                            case .permissions:
                                StandingPermissionsView(store: store)
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
                .tabItem {
                    Label("More", systemImage: "ellipsis")
                }
                .tag(NoriTab.more)
            }
            .tint(NoriColors.accent)
            .background(NoriColors.canvas)

            if let toast = store.toastMessage {
                NoriToast(toast)
                    .padding(.top, 10)
                    .padding(.horizontal, 16)
                    .transition(.move(edge: .top).combined(with: .opacity))
                    .zIndex(100)
                    .allowsHitTesting(false)
            }
        }
        .animation(.spring(response: 0.28, dampingFraction: 0.85), value: store.toastMessage)
        .sheet(item: $presentedDocument) { doc in
            DocumentReaderSheet(store: store, itemID: doc.id)
                .presentationDetents([.large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
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
        case .activitySheet:
            jumpToNeedsYou()
        case .oauthCallback:
            break
        }
    }
}
