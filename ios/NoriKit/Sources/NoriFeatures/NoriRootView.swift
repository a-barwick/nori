import SwiftUI
import NoriCore
import NoriDesign

private struct DocumentPresentation: Identifiable, Hashable {
    let id: String
}

/// Root 5-tab navigation shell for Nori iOS.
public struct NoriRootView: View {
    @Environment(\.scenePhase) private var scenePhase
    @State private var store: NoriStore

    @State private var selectedTab: NoriTab = .chat
    @State private var workTabFilter: WorkTabFilter = .needsYou
    @State private var workPath: [String] = []
    @State private var appsPath: [AppsRoute] = []
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
    }

    private func refresh(for tab: NoriTab) async {
        switch tab {
        case .chat:
            async let sessionTask: Void = store.refreshSession()
            async let summaryTask: Void = store.refreshSummary()
            _ = await (sessionTask, summaryTask)
        case .work:
            await store.refreshWorkAndInteractions()
        case .apps:
            await store.refreshApps()
        case .library:
            await store.refreshLibrary()
        case .more:
            async let summaryTask: Void = store.refreshSummary()
            async let memoryTask: Void = store.refreshMemory()
            async let routinesGoalsTask: Void = store.refreshRoutinesAndGoals()
            async let permsPrefsTask: Void = store.refreshPermissionsAndPreferences()
            _ = await (summaryTask, memoryTask, routinesGoalsTask, permsPrefsTask)
        }
    }

    @ViewBuilder
    private var signedInBody: some View {
        ZStack(alignment: .top) {
            TabView(selection: $selectedTab) {
                ChatView(
                    store: store,
                    onOpenActivity: { isActivitySheetPresented = true },
                    onOpenDocument: openDocument,
                    onNavigateToNeedsYou: jumpToNeedsYou,
                    onOpenTask: openWorkTask
                )
                .tabItem {
                    Label("Chat", systemImage: "bubble.left")
                }
                .tag(NoriTab.chat)

                NavigationStack(path: $workPath) {
                    WorkView(
                        store: store,
                        selectedTab: $workTabFilter,
                        onOpenActivity: { isActivitySheetPresented = true },
                        onOpenDocument: openDocument,
                        onOpenRoutines: jumpToRoutines,
                        onOpenTask: openWorkTask
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

                NavigationStack(path: $appsPath) {
                    AppsView(store: store)
                        .navigationDestination(for: AppsRoute.self) { route in
                            switch route {
                            case .mcpDetail(let serverID):
                                MCPDetailView(store: store, serverID: serverID)
                            }
                        }
                }
                .tabItem {
                    Label("Apps", systemImage: "square.grid.2x2")
                }
                .badge(store.appsAttentionCount)
                .tag(NoriTab.apps)

                NavigationStack {
                    LibraryView(
                        store: store,
                        onOpenDocument: openDocument,
                        onSelectSearchHit: handleSearchHit
                    )
                }
                .tabItem {
                    Label("Library", systemImage: "folder")
                }
                .tag(NoriTab.library)

                NavigationStack(path: $morePath) {
                    MoreHubView(store: store)
                        .navigationDestination(for: MoreRoute.self) { route in
                            switch route {
                            case .memory:
                                MemoryView(store: store)
                            case .routines:
                                RoutinesView(store: store)
                            case .goals:
                                GoalsView(store: store, onJumpToNeedsYou: jumpToNeedsYou)
                            case .settings(let section):
                                SettingsView(store: store, initialSection: section)
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

            // Floating NoriToast banner
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
        }
        .sheet(item: $presentedDocument) { doc in
            DocumentReaderSheet(store: store, itemID: doc.id)
                .presentationDetents([.large])
                .presentationDragIndicator(.visible)
        }
    }

    // MARK: - Cross-Tab Navigation Helpers

    private func openDocument(_ item: NoriCore.LibraryItem) {
        presentedDocument = DocumentPresentation(id: item.id)
    }

    private func jumpToNeedsYou() {
        isActivitySheetPresented = false
        presentedDocument = nil
        workPath = []
        workTabFilter = .needsYou
        selectedTab = .work
    }

    private func jumpToRoutines() {
        isActivitySheetPresented = false
        presentedDocument = nil
        selectedTab = .more
        morePath = [.routines]
    }

    private func openWorkTask(_ taskID: String) {
        isActivitySheetPresented = false
        presentedDocument = nil
        selectedTab = .work
        workPath = [taskID]
    }

    private func handleSearchHit(_ hit: SearchHit) {
        if let jump = hit.jump {
            switch jump.kind {
            case "conversation":
                selectedTab = .chat
                return
            case "task":
                if let taskID = jump.taskID {
                    openWorkTask(taskID)
                    return
                }
            case "library":
                if let itemID = jump.itemID {
                    presentedDocument = DocumentPresentation(id: itemID)
                    return
                }
            case "goal":
                selectedTab = .more
                morePath = [.goals]
                return
            case "automation":
                selectedTab = .more
                morePath = [.routines]
                return
            case "memory":
                selectedTab = .more
                morePath = [.memory]
                return
            default:
                break
            }
        }

        switch hit.kind {
        case .message:
            selectedTab = .chat
        case .task:
            openWorkTask(hit.id)
        case .result, .file:
            presentedDocument = DocumentPresentation(id: hit.id)
        case .goal:
            selectedTab = .more
            morePath = [.goals]
        case .automation:
            selectedTab = .more
            morePath = [.routines]
        case .memory:
            selectedTab = .more
            morePath = [.memory]
        }
    }

    private func applyDeepLink(_ link: NoriDeepLink) {
        switch link {
        case .tab(let tab):
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = tab
            if tab == .work {
                workPath = []
                workTabFilter = .needsYou
            } else if tab == .apps {
                appsPath = []
            } else if tab == .more {
                morePath = []
            }
        case .activitySheet:
            presentedDocument = nil
            isActivitySheetPresented = true
        case .memory:
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = .more
            morePath = [.memory]
        case .routines:
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = .more
            morePath = [.routines]
        case .goals:
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = .more
            morePath = [.goals]
        case .settings:
            isActivitySheetPresented = false
            presentedDocument = nil
            selectedTab = .more
            morePath = [.settings(nil)]
        case .workDetail(let taskID):
            openWorkTask(taskID)
        case .document(let itemID):
            isActivitySheetPresented = false
            presentedDocument = DocumentPresentation(id: itemID)
        case .oauthCallback:
            break
        }
    }
}
