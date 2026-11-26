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
    @State private var workTabFilter: WorkTabFilter = .tasks
    @State private var workPath: [String] = []
    @State private var morePath: [MoreRoute] = []

    @State private var isActivitySheetPresented = false
    @State private var presentedDocument: DocumentPresentation?
    @State private var hasBackgrounded = false
    @State private var tabRefreshTask: Task<Void, Never>?

    public init(store: NoriStore) {
        self._store = State(initialValue: store)
    }

    public init() {
        self._store = State(initialValue: NoriStore())
    }

    public var body: some View {
        Group {
            if store.authState.isSignedIn {
                signedInBody
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
                store.pauseForBackground()
            case .active:
                guard hasBackgrounded else { return }
                hasBackgrounded = false
                guard store.authState.isSignedIn else { return }
                Task {
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
            }
        }
        .onChange(of: selectedTab) { _, nextTab in
            tabRefreshTask?.cancel()
            guard store.authState.isSignedIn else { return }
            tabRefreshTask = Task {
                await refresh(for: nextTab)
            }
        }
        .preferredColorScheme(appearance.colorScheme)
    }

    private func refresh(for tab: NoriTab) async {
        switch tab {
        case .chat:
            async let sessionTask: Void = store.refreshSession()
            async let summaryTask: Void = store.refreshSummary()
            _ = await (sessionTask, summaryTask)
        case .work:
            async let workTask: Void = store.refreshWorkAndInteractions()
            async let libraryTask: Void = store.refreshLibrary()
            async let routinesGoalsTask: Void = store.refreshRoutinesAndGoals()
            _ = await (workTask, libraryTask, routinesGoalsTask)
        case .more:
            async let summaryTask: Void = store.refreshSummary()
            async let appsTask: Void = store.refreshApps()
            async let memoryTask: Void = store.refreshMemory()
            async let permsPrefsTask: Void = store.refreshPermissionsAndPreferences()
            async let modelKeysTask: Void = store.refreshModelKeys()
            _ = await (summaryTask, appsTask, memoryTask, permsPrefsTask, modelKeysTask)
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
                    onOpenActivity: { isActivitySheetPresented = true },
                    onOpenDocument: openDocument,
                    onNavigateToNeedsYou: jumpToNeedsYou,
                    onOpenTask: openWorkTask
                )
                .tabItem {
                    NoriMark.tabBarIcon(selected: selectedTab == .chat)
                    Text("Chat")
                }
                .tag(NoriTab.chat)

                NavigationStack(path: $workPath) {
                    WorkView(
                        store: store,
                        selectedTab: $workTabFilter,
                        onOpenDocument: openDocument,
                        onOpenTask: openWorkTask,
                        onStartInChat: startInChat
                    )
                    .navigationDestination(for: String.self) { taskID in
                        WorkDetailView(
                            store: store,
                            taskID: taskID,
                            onOpenDocument: openDocument
                        )
                    }
                }
                .tabItem {
                    Label("Work", systemImage: "checklist")
                }
                .badge(store.needsYouCount)
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
                            case .apiKeys:
                                APIKeysSettingsView(store: store)
                            case .appearance:
                                AppearanceSettingsView()
                            case .account:
                                AccountSettingsView(store: store)
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
        .sheet(isPresented: $isActivitySheetPresented) {
            ActivitySheetView(
                store: store,
                onSelectTask: { taskID in
                    isActivitySheetPresented = false
                    openWorkTask(taskID)
                },
                onOpenDocument: { item in
                    isActivitySheetPresented = false
                    openDocument(item)
                }
            )
            .presentationDetents([.medium, .large])
            .presentationDragIndicator(.visible)
            .presentationBackground(NoriColors.sheetBackground)
            .presentationCornerRadius(NoriRadius.sheet)
        }
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
        isActivitySheetPresented = false
        presentedDocument = nil
        workPath = []
        workTabFilter = .tasks
        selectedTab = .work
    }

    private func openWorkTask(_ taskID: String) {
        isActivitySheetPresented = false
        presentedDocument = nil
        selectedTab = .work
        workPath = [taskID]
    }

    private func startInChat(draft: String) {
        isActivitySheetPresented = false
        presentedDocument = nil
        store.pendingComposerDraft = draft
        selectedTab = .chat
    }

    private func applyDeepLink(_ link: NoriDeepLink) {
        switch link {
        case .tab(let tab):
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = tab
            if tab == .work {
                workPath = []
                workTabFilter = .tasks
            } else if tab == .more {
                morePath = []
            }
        case .apps:
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = .more
            morePath = [.apps]
        case .memory:
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = .more
            morePath = [.memory]
        case .library:
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = .work
            workPath = []
            workTabFilter = .artifacts
        case .document(let itemID):
            isActivitySheetPresented = false
            presentedDocument = DocumentPresentation(id: itemID)
        case .routines:
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = .work
            workPath = []
            workTabFilter = .routines
        case .goals:
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = .work
            workPath = []
            workTabFilter = .goals
        case .settings:
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = .more
            morePath = []
        case .workDetail(let taskID):
            openWorkTask(taskID)
        case .activitySheet:
            presentedDocument = nil
            isActivitySheetPresented = true
        case .oauthCallback:
            break
        }
    }
}
