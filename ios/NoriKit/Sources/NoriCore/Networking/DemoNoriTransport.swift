import Foundation

/// Rich in-memory mock transport seeded with a realistic workspace scenario.
/// Allows exploring every iOS screen and interactive flow immediately without a running backend.
public actor DemoNoriTransport: NoriTransport {
    private var capabilities: Capabilities
    private var session: Session
    private var workItems: [WorkItem]
    private var workDetails: [String: WorkDetail]
    private var interactions: [Interaction]
    private var mcpServers: [MCPServer]
    private var mcpTools: [String: [MCPServerTool]]
    private var connections: [Connection]
    private var libraryItems: [LibraryItem]
    private var coreMemories: [MemoryFact]
    private var memoryLogs: [MemoryLog]
    private var workingMemory: WorkingMemory
    private var automations: [Automation]
    private var goals: [Goal]
    private var permissions: [PermissionRule]
    private var preferences: Preferences
    private var nextSequence: Int = 12
    private var replyDelayNanoseconds: UInt64

    public init(simulatedReplyDelayMs: UInt64 = 650) {
        self.replyDelayNanoseconds = simulatedReplyDelayMs * 1_000_000
        self.capabilities = Capabilities(
            contract: "nori-chat-2",
            conversationContract: "nori-conversations-1",
            service: "nori",
            persistence: "demo-memory",
            provider: "demo",
            model: "nori-demo-sonnet",
            conversationModes: ["assistant"],
            features: ["backgroundTasks": true, "jev": true],
            assistantContract: "nori-assistant-1",
            assistantFeatures: [
                "integrations": true,
                "connections": true,
                "mcp": true,
                "interactions": true,
                "work": true,
                "automations": true,
                "goals": true,
                "library": true,
                "search": true,
                "permissions": true,
                "preferences": true,
                "memory": true,
                "operations": true
            ],
            uploadAccept: UploadAccept(
                mediaTypes: ["text/csv", "application/pdf", "image/png", "image/jpeg", "text/plain", "text/markdown"],
                maxBytes: 10 * 1024 * 1024,
                extensions: ["csv", "pdf", "png", "jpg", "txt", "md"]
            )
        )

        // Seed demo messages
        let pagePart = MessagePart(
            id: "part-page-lib-1",
            type: "page",
            text: "Annual renewal across AWS, Datadog, and Linear totals $48,200 (+7.1% over Q4 budget). Two unused observability seats can save $3,600/yr if trimmed before Dec 1.",
            display: "page",
            fallback: "Q4 Vendor Renewal Brief ($48,200)",
            kind: "file",
            resource: CardResourceRef(kind: "result", id: "lib-1", revision: 2),
            title: "Q4 Vendor Renewal Brief",
            metrics: [
                PageMetric(label: "Total Renewal", value: "$48,200"),
                PageMetric(label: "Trim Savings", value: "-$3,600/yr")
            ]
        )

        let seededMessages: [Message] = [
            Message(
                id: "m1",
                runID: "task-1",
                role: .user,
                text: "Can you pull together the Q4 cloud vendor renewal numbers and email procurement if we're over $45k?",
                createdAt: "9:41 AM",
                separatorLabel: "Today · 9:41 AM"
            ),
            Message(
                id: "m2",
                runID: "task-1",
                role: .assistant,
                text: "On it. I checked the billing export and put the renewal comparison in a brief for you — total lands at **$48,200** (+7.1% YoY), so I drafted the note to procurement and queued it for your approval before sending.",
                createdAt: "9:42 AM"
            ),
            Message(
                id: "m3",
                runID: "task-1",
                role: .assistant,
                text: "Q4 Vendor Renewal Brief",
                parts: [pagePart],
                resultIDs: ["lib-1"],
                createdAt: "9:42 AM"
            ),
            Message(
                id: "m4",
                runID: "task-2",
                role: .user,
                text: "Nice catch on the unused seats. Also hold 45 minutes Friday afternoon for the design review.",
                createdAt: "9:44 AM",
                receipt: .saved
            ),
            Message(
                id: "m5",
                runID: "task-2",
                role: .assistant,
                text: "Started a background task to schedule Friday's design review. Quick check — should I put that on your Work or Personal calendar?",
                createdAt: "9:44 AM"
            )
        ]

        // Seed Interactions
        let int1 = Interaction(
            id: "int-1",
            revision: 1,
            kind: .approval,
            taskID: "task-1",
            instructionRevision: 1,
            conversationID: "assistant",
            status: .pending,
            prompt: "Send this?",
            payload: .approval(ApprovalPayload(
                summary: "Hi Procurement team,\n\nOur Q4 cloud & tooling renewal came in at $48,200 (+7.1% above the $45k threshold). We can bring it down to $44,600 by removing 2 unused Datadog seats before Dec 1.\n\nAttached is the line-item breakdown. Let me know if you want us to lock the trimmed quote.",
                effect: .sensitiveSend,
                account: ApprovalAccount(connectionID: "conn-gws", label: "alex@example.com (Google Workspace)"),
                resources: [ResourceRef(connectionID: "conn-gws", kind: "mail_draft", providerID: "draft-q4-renewal")],
                recipients: ["procurement@acme.com"],
                materialEffects: [
                    "Sends an external email that cannot be undone",
                    "Attaches Q4 Vendor Renewal Brief.pdf"
                ],
                summaryHead: "Send \"Q4 Vendor Renewal — $48.2k (over threshold)\""
            )),
            actionDigest: "sha256:q4-vendor-renewal-procurement-email-v1",
            expiresAt: "in 42m",
            createdAt: "9:42 AM",
            taskTitle: "Q4 vendor renewal & procurement email"
        )

        let int2 = Interaction(
            id: "int-2",
            revision: 1,
            kind: .question,
            taskID: "task-2",
            instructionRevision: 1,
            conversationID: "assistant",
            status: .pending,
            prompt: "Which calendar should I hold Friday's design review on?",
            payload: .question(QuestionPayload(
                input: .choice,
                options: [
                    QuestionOption(id: "cal-work", label: "Work · alex@example.com"),
                    QuestionOption(id: "cal-personal", label: "Personal · alex@personal.example")
                ]
            )),
            createdAt: "9:44 AM",
            taskTitle: "Schedule Friday design review"
        )

        self.interactions = [int1, int2]

        // Seed Tasks / WorkItems & WorkDetails
        let work1 = WorkItem(
            taskID: "task-1",
            title: "Q4 vendor renewal & procurement email",
            status: .running,
            bucket: .needsYou,
            instructionRevision: 1,
            origin: WorkOrigin(kind: .conversation, conversationID: "assistant", messageID: "m1"),
            interactionIDs: ["int-1"],
            progress: "Waiting for your approval to send email to procurement@acme.com",
            operations: WorkOperationsSummary(total: 3, uncertain: 0, failed: 0, submitted: 0),
            resultIDs: ["lib-1"],
            cost: WorkCost(amountUSD: 0.14, status: .known, budgetUSD: 1.00),
            startedAt: "6m ago"
        )
        let work2 = WorkItem(
            taskID: "task-2",
            title: "Schedule Friday design review",
            status: .running,
            bucket: .working,
            instructionRevision: 1,
            origin: WorkOrigin(kind: .conversation, conversationID: "assistant", messageID: "m4"),
            interactionIDs: ["int-2"],
            progress: "Checking attendee availability for Friday 1:00–4:00 PM PT",
            operations: WorkOperationsSummary(total: 1, uncertain: 0, failed: 0, submitted: 0),
            resultIDs: [],
            cost: WorkCost(amountUSD: 0.04, status: .known, budgetUSD: 1.00),
            startedAt: "2m ago"
        )
        let work3 = WorkItem(
            taskID: "task-3",
            title: "Summarize Linear sprint blockers",
            status: .completed,
            bucket: .finished,
            instructionRevision: 1,
            origin: WorkOrigin(kind: .automation, automationID: "auto-2"),
            interactionIDs: [],
            progress: "Posted 3 blockers and saved summary to Library",
            operations: WorkOperationsSummary(total: 1, uncertain: 0, failed: 0, submitted: 0),
            resultIDs: ["lib-2"],
            cost: WorkCost(amountUSD: 0.19, status: .known, budgetUSD: 1.00),
            startedAt: "Yesterday",
            finishedAt: "Yesterday"
        )
        let work4 = WorkItem(
            taskID: "task-4",
            title: "Deep audit of API latency logs",
            status: .failed,
            bucket: .finished,
            instructionRevision: 1,
            origin: WorkOrigin(kind: .conversation, conversationID: "assistant"),
            interactionIDs: [],
            progress: "Reached the $1.00 spending limit before finishing all 12 services.",
            operations: WorkOperationsSummary(total: 1, uncertain: 0, failed: 0, submitted: 0),
            resultIDs: [],
            cost: WorkCost(amountUSD: 1.00, status: .known, budgetUSD: 1.00),
            startedAt: "2d ago",
            finishedAt: "2d ago",
            error: ErrorDetail(code: "budget_exhausted", message: "Incomplete: reached spending limit ($1.00).", retryable: true)
        )

        self.workItems = [work1, work2, work3, work4]

        self.workDetails = [
            "task-1": WorkDetail(
                item: work1,
                operations: [
                    OperationProjection(id: "op-1", name: "files.read", effectClass: .read, outcome: .confirmed, accountSummary: "Uploaded file", actionSummary: "Read Q4_billing_export.csv", authoritySource: "user_request", createdAt: "6m ago"),
                    OperationProjection(id: "op-2", name: "library.save", effectClass: .reversibleWrite, outcome: .confirmed, accountSummary: "Nori Library", actionSummary: "Saved Q4 Vendor Renewal Brief (v2)", authoritySource: "internal", createdAt: "5m ago"),
                    OperationProjection(id: "op-3", name: "gmail.send", effectClass: .sensitiveSend, outcome: .pending, connectionID: "conn-gws", accountSummary: "alex@example.com", actionSummary: "Send email to procurement@acme.com", authoritySource: "awaiting approval", createdAt: "4m ago")
                ],
                interactions: [int1],
                events: [
                    RunEvent(id: "ev-1", kind: "accepted", text: "Accepted request from Chat (budget $1.00).", createdAt: "9:41 AM"),
                    RunEvent(id: "ev-2", kind: "progress", text: "Parsed 3 vendor contracts and compared against $45k cap.", createdAt: "9:41 AM"),
                    RunEvent(id: "ev-3", kind: "result", text: "Saved Q4 Vendor Renewal Brief to Library.", createdAt: "9:42 AM"),
                    RunEvent(id: "ev-4", kind: "waiting", text: "Paused for approval before sensitive_send to procurement@acme.com.", createdAt: "9:42 AM")
                ]
            ),
            "task-2": WorkDetail(
                item: work2,
                operations: [
                    OperationProjection(id: "op-4", name: "gcal.freebusy", effectClass: .read, outcome: .confirmed, connectionID: "conn-gws", accountSummary: "Google Calendar", actionSummary: "Read team free/busy for Fri Nov 27", authoritySource: "permission_rule", createdAt: "1m ago")
                ],
                interactions: [int2],
                events: [
                    RunEvent(id: "ev-5", kind: "progress", text: "Checked availability across 4 attendees — 2:00 PM PT is open.", createdAt: "9:44 AM"),
                    RunEvent(id: "ev-6", kind: "waiting", text: "Asked which calendar to create the hold on.", createdAt: "9:44 AM")
                ]
            ),
            "task-3": WorkDetail(
                item: work3,
                operations: [
                    OperationProjection(id: "op-5", name: "mcp_linear__list_issues", effectClass: .read, outcome: .confirmed, accountSummary: "Linear MCP", actionSummary: "Query Linear issues (label: blocker)", authoritySource: "automation", createdAt: "Yesterday")
                ],
                interactions: [],
                result: "Posted 3 sprint blockers and saved Sprint 42 Blocker Digest to Library.",
                events: [
                    RunEvent(id: "ev-7", kind: "accepted", text: "Triggered by Morning sprint check automation.", createdAt: "Yesterday 8:00 AM"),
                    RunEvent(id: "ev-8", kind: "completed", text: "Completed with 3 open blockers reported.", createdAt: "Yesterday 8:01 AM")
                ]
            ),
            "task-4": WorkDetail(
                item: work4,
                operations: [
                    OperationProjection(id: "op-6", name: "mcp_gateway__trace_audit", effectClass: .read, outcome: .confirmed, accountSummary: "Workspace Gateway", actionSummary: "Inspected 8 of 12 service traces", authoritySource: "user_request", createdAt: "2d ago")
                ],
                interactions: [],
                events: [
                    RunEvent(id: "ev-9", kind: "failed", text: "Stopped automatically at recorded $1.00 task budget.", createdAt: "2d ago")
                ]
            )
        ]

        let seededRuns: [Run] = workItems.map { w in
            Run(
                id: w.taskID,
                status: w.status,
                cancelRequested: w.cancelRequested,
                request: w.title,
                progress: w.progress,
                spentUSD: w.cost.amountUSD,
                budgetUSD: w.cost.budgetUSD,
                startedAt: w.startedAt,
                finishedAt: w.finishedAt,
                error: w.error?.message
            )
        }

        self.session = Session(
            messages: seededMessages,
            runs: seededRuns,
            paused: false,
            conversation: Conversation(pending: false, processing: false, revision: 5, consumed: 5),
            position: 11
        )

        // Seed MCP Servers & Tools
        self.mcpServers = [
            MCPServer(
                id: "mcp-gateway",
                revision: 1,
                name: "Workspace MCP Gateway",
                transport: .http,
                endpoint: "https://mcp.example.com/v1/stream",
                enabled: true,
                exposure: MCPExposureConfig(default: .discoverable),
                observed: MCPObservedState(
                    status: .connected,
                    protocolVersion: "2025-03-26",
                    capabilities: ["tools", "resources"],
                    toolCount: 4,
                    lastSuccessAt: "4m ago",
                    credential: "present"
                ),
                auth: .oauth
            ),
            MCPServer(
                id: "mcp-linear",
                revision: 1,
                name: "Linear Workspace",
                transport: .http,
                endpoint: "https://mcp.linear.app/sse",
                enabled: true,
                exposure: MCPExposureConfig(default: .discoverable),
                observed: MCPObservedState(
                    status: .needsSignIn,
                    protocolVersion: "2025-03-26",
                    capabilities: ["tools"],
                    toolCount: 3,
                    error: ErrorDetail(code: "oauth_expired", message: "OAuth token expired — sign in to reconnect.", retryable: true),
                    credential: "missing"
                ),
                auth: .oauth
            ),
            MCPServer(
                id: "mcp-notes",
                revision: 1,
                name: "Local Notes (stdio)",
                transport: .stdio,
                endpoint: "nori-mcp-notes --vault ~/Notes",
                enabled: true,
                exposure: MCPExposureConfig(default: .direct),
                observed: MCPObservedState(
                    status: .connected,
                    protocolVersion: "2025-03-26",
                    capabilities: ["tools", "resources"],
                    toolCount: 2,
                    lastSuccessAt: "Yesterday",
                    credential: "present"
                ),
                auth: MCPAuthKind.none
            )
        ]

        self.mcpTools = [
            "mcp-gateway": [
                MCPServerTool(name: "mcp_gateway__directory_lookup", title: "Directory lookup", exposure: .direct),
                MCPServerTool(name: "mcp_gateway__billing_query", title: "Billing export query", exposure: .discoverable),
                MCPServerTool(name: "mcp_gateway__ticket_create", title: "Create IT ticket", exposure: .discoverable),
                MCPServerTool(name: "mcp_gateway__wiki_search", title: "Internal wiki search", exposure: .direct)
            ],
            "mcp-linear": [
                MCPServerTool(name: "mcp_linear__list_issues", title: "List issues", exposure: .direct, availability: .needsConnection),
                MCPServerTool(name: "mcp_linear__update_issue", title: "Update issue", exposure: .discoverable, availability: .needsConnection),
                MCPServerTool(name: "mcp_linear__comment", title: "Post comment", exposure: .disabled, availability: .disabled)
            ],
            "mcp-notes": [
                MCPServerTool(name: "mcp_notes__search", title: "Search local markdown", exposure: .direct),
                MCPServerTool(name: "mcp_notes__append", title: "Append daily note", exposure: .discoverable)
            ]
        ]

        // Seed Connections
        self.connections = [
            Connection(
                id: "conn-gws",
                revision: 1,
                integrationID: "google-workspace",
                title: "Google Workspace",
                account: ConnectionAccount(providerID: "gws-1", label: "alex@example.com"),
                status: .connected,
                grantedScopes: ["gmail.readonly", "gmail.send", "calendar.events"],
                health: ConnectionHealth(code: "ready", message: "Last used 1m ago", lastSuccessAt: "1m ago", recovery: "none"),
                defaults: ["calendar.create", "mail.send"],
                capabilities: ["calendar.read", "calendar.write", "mail.read", "mail.send"]
            ),
            Connection(
                id: "conn-slack",
                revision: 1,
                integrationID: "slack",
                title: "Slack",
                account: ConnectionAccount(providerID: "slack-1", label: "Acme Corp"),
                status: .connected,
                grantedScopes: ["channels:read", "chat:write"],
                health: ConnectionHealth(code: "ready", message: "Last used 2h ago", lastSuccessAt: "2h ago", recovery: "none"),
                capabilities: ["chat.read", "chat.post"]
            )
        ]

        // Seed Library Items
        self.libraryItems = [
            LibraryItem(
                id: "lib-1",
                rootID: "lib-1",
                version: 2,
                kind: .document,
                title: "Q4 Vendor Renewal Brief",
                mediaType: "text/markdown",
                taskID: "task-1",
                conversationID: "assistant",
                goalID: "goal-1",
                provenance: Provenance(content: .generated),
                createdAt: "5m ago",
                collection: "Finance",
                previewText: "Annual renewal totals $48,200 (+7.1% YoY). Trimming 2 unused Datadog seats saves $3,600/yr.",
                revision: 2,
                bodyText: "# Q4 Vendor Renewal Brief\n\nTotal projected Q4 renewal is **$48,200** (+7.1% vs $45,000 cap).\n\n- **AWS Production:** $29,400 (steady)\n- **Datadog APM:** $14,200 (+$3,600 from 2 inactive seats)\n- **Linear Enterprise:** $4,600\n\n### Recommendation\nTrim the 2 inactive Datadog seats before Dec 1 to land at **$44,600**, under the procurement escalation cap."
            ),
            LibraryItem(
                id: "lib-2",
                rootID: "lib-2",
                version: 1,
                kind: .document,
                title: "Sprint 42 Blocker Digest",
                mediaType: "text/markdown",
                taskID: "task-3",
                automationID: "auto-2",
                provenance: Provenance(content: .generated),
                createdAt: "Yesterday",
                collection: "Engineering",
                previewText: "3 active blockers across Postgres lease repair, OIDC token refresh, and iOS push relay.",
                revision: 1,
                bodyText: "# Sprint 42 Blocker Digest\n\n1. **Postgres lease repair:** verified in DST suite.\n2. **OIDC refresh:** needs PKCE mobile redirect URI.\n3. **APNs push relay:** awaiting team cert."
            ),
            LibraryItem(
                id: "lib-3",
                rootID: "lib-3",
                version: 1,
                kind: .file,
                title: "Q4_billing_export.csv",
                name: "Q4_billing_export.csv",
                mediaType: "text/csv",
                size: 18_840,
                taskID: "task-1",
                provenance: Provenance(content: .uploaded),
                createdAt: "6m ago",
                collection: "Finance",
                previewText: "42 rows · 18.4 KB · CSV spreadsheet",
                revision: 1,
                bodyText: "vendor,plan,annual_usd,seats_active,seats_billed\nAWS,Enterprise,29400,18,18\nDatadog,Pro,14200,6,8\nLinear,Plus,4600,24,24"
            ),
            LibraryItem(
                id: "lib-4",
                rootID: "lib-4",
                version: 1,
                kind: .event,
                title: "iOS Architecture Review",
                provenance: Provenance(content: .live, connectionStatus: .connected),
                createdAt: "Tomorrow · 2:00 PM",
                dataKind: "calendar_event",
                collection: "Calendar",
                previewText: "Fri Nov 27 · 2:00–2:45 PM PT · Conf Room B",
                revision: 1,
                bodyText: "**iOS Architecture Review**\nFri Nov 27 · 2:00–2:45 PM PT\nLocation: Conf Room B\nCalendar: Work (alex@example.com)"
            )
        ]

        // Seed 3-Layer Memory
        self.coreMemories = [
            MemoryFact(id: "mem-1", revision: 1, text: "Prefers concise bullet summaries with exact dollar amounts and deadlines up front.", section: "Preferences", pinned: true, provenance: .userExplicit, createdAt: "3w ago"),
            MemoryFact(id: "mem-2", revision: 1, text: "Escalation threshold for vendor renewals is $45,000 — email procurement@acme.com above that.", section: "Decisions", pinned: true, provenance: .userExplicit, createdAt: "12d ago"),
            MemoryFact(id: "mem-3", revision: 1, text: "Working hours are Mon–Fri 9:00 AM–5:30 PM America/Los_Angeles; protect Friday mornings for deep work.", section: "Preferences", pinned: false, provenance: .dreamCurated, createdAt: "5d ago"),
            MemoryFact(id: "mem-4", revision: 1, text: "Default calendar for design reviews is Work (alex@example.com).", section: "Context", pinned: false, provenance: .preCompactFlush, createdAt: "2d ago")
        ]

        self.memoryLogs = [
            MemoryLog(
                date: "2026-11-23",
                revision: 1,
                entries: [
                    MemoryLogEntry(id: "log-1", section: "Finance", text: "Q4 Datadog contract has 2 unused seats billed at $150/mo each.", provenance: .taskOutcome, runID: "task-1", createdAt: "9:42 AM", promoted: false),
                    MemoryLogEntry(id: "log-2", section: "Product", text: "User wants native SwiftUI iOS test client matching Nori lavender-violet design system.", provenance: .preCompactFlush, createdAt: "9:30 AM", promoted: false)
                ],
                updatedAt: "9:42 AM"
            ),
            MemoryLog(
                date: "2026-11-22",
                revision: 1,
                entries: [
                    MemoryLogEntry(id: "log-3", section: "Engineering", text: "Coupled memory & compaction migration passed full chaos DST suite.", provenance: .dreamCurated, createdAt: "Yesterday", promoted: true)
                ],
                updatedAt: "Yesterday"
            )
        ]

        self.workingMemory = WorkingMemory(
            revision: 1,
            items: [
                WorkingMemoryItem(id: "wm-1", kind: .constraint, quote: "Do not send procurement email without explicit approval card sign-off.", inputID: "m1"),
                WorkingMemoryItem(id: "wm-2", kind: .openQuestion, quote: "Confirm Work vs Personal calendar for Friday design review.", inputID: "m4"),
                WorkingMemoryItem(id: "wm-3", kind: .commitment, quote: "Trim 2 Datadog seats before Dec 1 renewal cutoff.", inputID: "m4")
            ],
            gist: "Reviewing Q4 vendor renewal ($48.2k) and holding Friday afternoon for design review.",
            turns: 5,
            model: "nori-demo-sonnet",
            coreBytes: 6960,
            coreBudgetBytes: 24 * 1024,
            updatedAt: "Just now"
        )

        // Seed Automations
        self.automations = [
            Automation(
                id: "auto-1",
                revision: 1,
                title: "Morning brief",
                instruction: "Summarize today's calendar, urgent email, and open approvals.",
                status: .enabled,
                trigger: AutomationTrigger(kind: "recurring", rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", displayCadence: "Weekdays at 8:00 AM"),
                budgetUSD: 0.50,
                nextAt: "tomorrow 8:00 AM"
            ),
            Automation(
                id: "auto-2",
                revision: 1,
                title: "Linear sprint blocker check",
                instruction: "Query Linear issues labeled blocker and save a digest to Library.",
                status: .enabled,
                trigger: AutomationTrigger(kind: "recurring", rrule: "FREQ=WEEKLY;BYDAY=MO,WE,FR", displayCadence: "Mon, Wed, Fri at 9:00 AM"),
                budgetUSD: 1.00,
                nextAt: "Wed 9:00 AM"
            ),
            Automation(
                id: "auto-3",
                revision: 1,
                title: "Weekly cloud spend watch",
                instruction: "Compare weekly AWS & Datadog spend against budget thresholds.",
                status: .paused,
                trigger: AutomationTrigger(kind: "recurring", rrule: "FREQ=WEEKLY;BYDAY=SU", displayCadence: "Sundays at 6:00 PM"),
                budgetUSD: 0.75,
                nextAt: "Sun 6:00 PM"
            )
        ]

        // Seed Goals
        self.goals = [
            Goal(
                id: "goal-1",
                revision: 1,
                title: "Close Q4 vendor renewals under $45k",
                outcome: "Finalize AWS, Datadog, and Linear renewals under $45,000 total before Dec 1.",
                completionCriteria: [
                    GoalCriterion(id: "gc-1", text: "Audit AWS, Datadog, and Linear line items", met: true),
                    GoalCriterion(id: "gc-2", text: "Remove 2 inactive Datadog seats", met: false),
                    GoalCriterion(id: "gc-3", text: "Receive written confirmation from Procurement", met: false)
                ],
                status: .active,
                progress: "Brief drafted; awaiting approval to email Procurement.",
                blockers: [GoalBlocker(text: "Waiting for your approval on procurement email", interactionID: "int-1")],
                linked: GoalLinked(taskIDs: ["task-1"], resultIDs: ["lib-1"]),
                deadline: "Dec 1"
            ),
            Goal(
                id: "goal-2",
                revision: 1,
                title: "Ship Nori iOS TestFlight build",
                outcome: "Native SwiftUI iOS client verified against nori-local and ready for TestFlight.",
                completionCriteria: [
                    GoalCriterion(id: "gc-4", text: "Review interactive HTML iOS mockups", met: true),
                    GoalCriterion(id: "gc-5", text: "Connect SwiftUI app to nori-local SSE stream", met: false),
                    GoalCriterion(id: "gc-6", text: "Verify approval cards & OAuth on device", met: false)
                ],
                status: .active,
                progress: "Mockups approved; native SwiftUI app in progress.",
                deadline: "Dec 12"
            )
        ]

        // Seed Permissions
        self.permissions = [
            PermissionRule(id: "perm-1", revision: 1, effect: .deny, toolIDs: ["*"], connectionIDs: ["*"], resourceIDs: ["calendar_event"], actionClasses: [.reversibleWrite, .sensitiveSend], title: "Never delete calendar events", summary: "All tools & MCP servers"),
            PermissionRule(id: "perm-2", revision: 1, effect: .allow, toolIDs: ["gcal.freebusy"], connectionIDs: ["conn-gws"], resourceIDs: ["*"], actionClasses: [.read], title: "Read Google Calendar availability", summary: "Read · alex@example.com"),
            PermissionRule(id: "perm-3", revision: 1, effect: .allow, toolIDs: ["mcp_linear__list_issues"], connectionIDs: ["mcp-linear"], resourceIDs: ["*"], actionClasses: [.read], automationID: "auto-2", title: "Query Linear sprint blockers", summary: "Read · Linear MCP · Automation")
        ]

        // Seed Preferences
        self.preferences = Preferences(
            revision: 1,
            timezone: "America/Los_Angeles",
            locale: "en-US",
            workingHours: [
                WorkingHoursSlot(day: 1, start: "09:00", end: "17:30"),
                WorkingHoursSlot(day: 2, start: "09:00", end: "17:30"),
                WorkingHoursSlot(day: 3, start: "09:00", end: "17:30"),
                WorkingHoursSlot(day: 4, start: "09:00", end: "17:30"),
                WorkingHoursSlot(day: 5, start: "09:00", end: "17:30")
            ],
            quietHours: QuietHours(start: "21:00", end: "08:00"),
            defaultDestinationIDs: ["ios-push"],
            accountDefaults: ["calendar.create": "conn-gws", "mail.send": "conn-gws"],
            budgets: BudgetPreferences(taskUSD: 1.00, monthlyUSD: 25.00),
            input: InputPreferences(dictationLanguage: "en-US")
        )
    }

    // MARK: - Demo Helpers

    public func triggerDemoApproval() -> Interaction {
        let id = "int-demo-\(UUID().uuidString.prefix(6).lowercased())"
        let taskID = "task-demo-\(UUID().uuidString.prefix(6).lowercased())"
        let interaction = Interaction(
            id: id,
            revision: 1,
            kind: .approval,
            taskID: taskID,
            instructionRevision: 1,
            conversationID: "assistant",
            status: .pending,
            prompt: "Send this?",
            payload: .approval(ApprovalPayload(
                summary: "Hi Datadog billing,\n\nPlease remove 2 inactive APM seats effective immediately ahead of our Dec 1 renewal (reducing our annual renewal from $14,200 to $10,600).\n\nThanks,\nAlex",
                effect: .sensitiveSend,
                account: ApprovalAccount(connectionID: "conn-gws", label: "alex@example.com (Google Workspace)"),
                recipients: ["billing@datadoghq.com"],
                materialEffects: [
                    "Sends an external email to Datadog billing",
                    "Changes seat count ahead of Dec 1 renewal"
                ],
                summaryHead: "Send \"Remove 2 unused Datadog seats before Dec 1\""
            )),
            actionDigest: "sha256:demo-\(id)",
            expiresAt: "in 59m",
            createdAt: "Just now",
            taskTitle: "Trim 2 unused Datadog seats"
        )
        interactions.insert(interaction, at: 0)

        let task = WorkItem(
            taskID: taskID,
            title: "Trim 2 unused Datadog seats",
            status: .running,
            bucket: .needsYou,
            origin: WorkOrigin(kind: .conversation, conversationID: "assistant"),
            interactionIDs: [id],
            progress: "Waiting for your approval to email billing@datadoghq.com",
            operations: WorkOperationsSummary(total: 1),
            cost: WorkCost(amountUSD: 0.03, status: .known, budgetUSD: 1.00),
            startedAt: "Just now"
        )
        workItems.insert(task, at: 0)
        workDetails[taskID] = WorkDetail(
            item: task,
            operations: [
                OperationProjection(id: "op-\(id)", name: "gmail.send", effectClass: .sensitiveSend, outcome: .pending, accountSummary: "alex@example.com", actionSummary: "Send seat trim request to billing@datadoghq.com", authoritySource: "awaiting approval", createdAt: "Just now")
            ],
            interactions: [interaction],
            events: [RunEvent(kind: "waiting", text: "Prepared email to billing@datadoghq.com and paused for approval.", createdAt: "Just now")]
        )
        return interaction
    }

    // MARK: - NoriTransport Implementation

    public func ping() async -> HealthCheckResult {
        HealthCheckResult(ok: true, status: "ok (demo)", model: capabilities.model, latencyMs: 12)
    }

    public func fetchCapabilities() async throws -> Capabilities {
        capabilities
    }

    public func fetchSession(before cursor: String?) async throws -> Session {
        session
    }

    public func fetchFullSnapshot() async throws -> NoriFullSnapshot {
        NoriFullSnapshot(
            capabilities: capabilities,
            session: session,
            workItems: workItems,
            interactions: interactions,
            mcpServers: mcpServers,
            mcpTools: mcpTools,
            connections: connections,
            libraryItems: libraryItems,
            coreMemories: coreMemories,
            memoryLogs: memoryLogs,
            workingMemory: workingMemory,
            automations: automations,
            goals: goals,
            permissions: permissions,
            preferences: preferences
        )
    }

    public nonisolated func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> {
        // Demo mode mutates state directly through transport methods without needing a live network socket.
        AsyncThrowingStream { continuation in
            continuation.onTermination = { _ in }
        }
    }

    public func sendTurn(id: String, message: String, attachmentIDs: [String]) async throws -> Session {
        let trimmed = message.trimmingCharacters(in: .whitespacesAndNewlines)
        let nowLabel = Self.shortTimeString()
        var parts: [MessagePart] = []
        if !trimmed.isEmpty {
            parts.append(MessagePart(type: "text", text: trimmed))
        }
        for attID in attachmentIDs {
            parts.append(MessagePart(id: attID, type: "attachment", name: "Attachment \(attID.prefix(6))"))
        }

        let userMsg = Message(
            id: id,
            turnID: id,
            role: .user,
            text: trimmed,
            parts: parts.isEmpty ? nil : parts,
            createdAt: nowLabel,
            receipt: .saved
        )
        if !session.messages.contains(where: { $0.id == id }) {
            session.messages.append(userMsg)
        }

        if replyDelayNanoseconds > 0 {
            try? await Task.sleep(nanoseconds: replyDelayNanoseconds)
        }

        let replyText = Self.demoReply(for: trimmed)
        let assistantMsg = Message(
            id: "nori-\(UUID().uuidString.prefix(8).lowercased())",
            turnID: id,
            role: .assistant,
            text: replyText,
            createdAt: Self.shortTimeString()
        )
        session.messages.append(assistantMsg)
        nextSequence += 1
        session.position = nextSequence
        return session
    }

    public func sendTaskCommand(_ command: TaskCommand) async throws -> TaskCommandResponse {
        guard let idx = workItems.firstIndex(where: { $0.taskID == command.runID }) else {
            throw NoriAPIError(message: "Task not found.", statusCode: 404, code: "not_found")
        }
        var item = workItems[idx]
        item.instructionRevision += 1
        switch command.kind {
        case .steer:
            let text = command.text ?? ""
            item.progress = "Steered: \(text)"
            workDetails[item.taskID]?.events?.append(
                RunEvent(kind: "steer", text: "You steered task: \"\(text)\"", createdAt: Self.shortTimeString())
            )
        case .stop:
            item.status = .cancelled
            item.bucket = .finished
            item.cancelRequested = true
            item.progress = "Stopped at your request."
            item.finishedAt = "Just now"
            workDetails[item.taskID]?.events?.append(
                RunEvent(kind: "stopped", text: "Stopped at your request.", createdAt: Self.shortTimeString())
            )
        case .answer, .approve, .decline, .continue:
            item.progress = "Updated from your command."
        }
        workItems[idx] = item
        workDetails[item.taskID]?.item = item

        let run = Run(
            id: item.taskID,
            status: item.status,
            cancelRequested: item.cancelRequested,
            request: item.title,
            progress: item.progress,
            spentUSD: item.cost.amountUSD,
            budgetUSD: item.cost.budgetUSD,
            startedAt: item.startedAt,
            finishedAt: item.finishedAt
        )
        return TaskCommandResponse(command: nil, run: run, accepted: true, retryable: false, error: nil)
    }

    public func uploadFile(id: String, filename: String, mediaType: String, data: Data) async throws -> Attachment {
        Attachment(id: id, name: filename, mediaType: mediaType, size: data.count)
    }

    public func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem] {
        guard let bucket else { return workItems }
        return workItems.filter { $0.bucket == bucket }
    }

    public func fetchWorkDetail(taskID: String) async throws -> WorkDetail {
        if let detail = workDetails[taskID] {
            return detail
        }
        if let item = workItems.first(where: { $0.taskID == taskID }) {
            return WorkDetail(item: item)
        }
        throw NoriAPIError(message: "Task not found.", statusCode: 404, code: "not_found")
    }

    public func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem> {
        _ = try await sendTaskCommand(TaskCommand(id: requestID, runID: taskID, kind: .stop))
        guard let item = workItems.first(where: { $0.taskID == taskID }) else {
            throw NoriAPIError(message: "Task not found.", statusCode: 404, code: "not_found")
        }
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: item)
    }

    public func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem> {
        guard let original = workItems.first(where: { $0.taskID == taskID }) else {
            throw NoriAPIError(message: "Task not found.", statusCode: 404, code: "not_found")
        }
        let newID = "task-cont-\(UUID().uuidString.prefix(6).lowercased())"
        let continued = WorkItem(
            taskID: newID,
            title: original.title,
            status: .running,
            bucket: .working,
            origin: WorkOrigin(kind: .conversation, conversationID: "assistant", continuesTaskID: original.taskID),
            progress: "Continuing remaining 4 of 12 service traces with $\(String(format: "%.2f", budgetUSD)) budget…",
            operations: original.operations,
            cost: WorkCost(amountUSD: 0.02, status: .known, budgetUSD: budgetUSD),
            startedAt: "Just now"
        )
        workItems.insert(continued, at: 0)
        workDetails[newID] = WorkDetail(
            item: continued,
            operations: workDetails[taskID]?.operations ?? [],
            interactions: [],
            events: [
                RunEvent(kind: "accepted", text: "Continued from previous run with $\(String(format: "%.2f", budgetUSD)) budget.", createdAt: Self.shortTimeString())
            ]
        )
        return MutationReceipt(requestID: requestID, accepted: true, status: .queued, resource: continued, work: WorkRef(taskID: newID))
    }

    public func fetchInteractions() async throws -> [Interaction] {
        interactions
    }

    public func respondToInteraction(
        id: String,
        expectedRevision: Int,
        answer: InteractionAnswer,
        requestID: String
    ) async throws -> MutationReceipt<Interaction> {
        guard let idx = interactions.firstIndex(where: { $0.id == id }) else {
            throw NoriAPIError(message: "Interaction not found.", statusCode: 404, code: "not_found")
        }
        var interaction = interactions[idx]
        guard interaction.status == .pending else {
            throw NoriAPIError(message: "This item was already resolved.", statusCode: 409, code: "interaction_stale")
        }
        interaction.status = .resolved
        interaction.revision += 1
        interaction.resolvedAt = Self.shortTimeString()

        switch answer {
        case .approve:
            interaction.resolution = InteractionResolution(kind: .approve, text: "Approved", at: Self.shortTimeString())
            if let taskID = interaction.taskID, let tIdx = workItems.firstIndex(where: { $0.taskID == taskID }) {
                workItems[tIdx].status = .completed
                workItems[tIdx].bucket = .finished
                workItems[tIdx].progress = "Approved and sent cleanly."
                workItems[tIdx].finishedAt = "Just now"
                workDetails[taskID]?.item = workItems[tIdx]
                if var ops = workDetails[taskID]?.operations {
                    for i in ops.indices where ops[i].outcome == .pending {
                        ops[i].outcome = .confirmed
                        ops[i].authoritySource = "approval"
                    }
                    workDetails[taskID]?.operations = ops
                }
                workDetails[taskID]?.events?.append(RunEvent(kind: "approved", text: "Approved by you — action confirmed.", createdAt: Self.shortTimeString()))
            }
            // Clear matching goal blocker if present
            for gIdx in goals.indices {
                goals[gIdx].blockers.removeAll { $0.interactionID == id }
            }
            session.messages.append(
                Message(
                    id: "nori-act-\(UUID().uuidString.prefix(6).lowercased())",
                    runID: interaction.taskID ?? "",
                    role: .assistant,
                    text: "Sent! I completed \"\(interaction.taskTitle ?? "the approved action")\" and logged the confirmation in Work.",
                    createdAt: Self.shortTimeString()
                )
            )
        case .decline:
            interaction.resolution = InteractionResolution(kind: .decline, text: "Declined", at: Self.shortTimeString())
            if let taskID = interaction.taskID, let tIdx = workItems.firstIndex(where: { $0.taskID == taskID }) {
                workItems[tIdx].status = .cancelled
                workItems[tIdx].bucket = .finished
                workItems[tIdx].progress = "Declined — nothing was sent."
                workItems[tIdx].finishedAt = "Just now"
                workDetails[taskID]?.item = workItems[tIdx]
                workDetails[taskID]?.events?.append(RunEvent(kind: "declined", text: "Declined by you — nothing was sent.", createdAt: Self.shortTimeString()))
            }
        case .answer(let text, _):
            interaction.resolution = InteractionResolution(kind: .answer, text: text, at: Self.shortTimeString())
            if let taskID = interaction.taskID, let tIdx = workItems.firstIndex(where: { $0.taskID == taskID }) {
                workItems[tIdx].status = .completed
                workItems[tIdx].bucket = .finished
                workItems[tIdx].progress = "Held Friday 2:00–2:45 PM PT using \(text)."
                workItems[tIdx].finishedAt = "Just now"
                workDetails[taskID]?.item = workItems[tIdx]
                workDetails[taskID]?.events?.append(RunEvent(kind: "answered", text: "You answered: \(text). Calendar hold created.", createdAt: Self.shortTimeString()))
            }
            session.messages.append(
                Message(
                    id: "nori-ans-\(UUID().uuidString.prefix(6).lowercased())",
                    runID: interaction.taskID ?? "",
                    role: .assistant,
                    text: "Got it — I put Friday's design review (2:00–2:45 PM PT) on **\(text)**.",
                    createdAt: Self.shortTimeString()
                )
            )
        case .chooseAccount(let connID, _):
            interaction.resolution = InteractionResolution(kind: .chooseAccount, connectionID: connID, at: Self.shortTimeString())
        }

        interactions[idx] = interaction
        if let taskID = interaction.taskID, var detail = workDetails[taskID] {
            detail.interactions = detail.interactions.map { $0.id == id ? interaction : $0 }
            workDetails[taskID] = detail
        }
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: interaction)
    }

    // MARK: - Apps: MCP & Connections

    public func fetchMCPServers() async throws -> [MCPServer] {
        mcpServers
    }

    public func fetchMCPTools(serverID: String) async throws -> [MCPServerTool] {
        mcpTools[serverID] ?? []
    }

    public func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer> {
        let id = "mcp-\(UUID().uuidString.prefix(6).lowercased())"
        let server = MCPServer(
            id: id,
            revision: 1,
            name: input.name,
            transport: input.transport,
            endpoint: input.endpoint,
            enabled: input.enabled,
            exposure: input.exposure,
            observed: MCPObservedState(
                status: input.auth == .oauth ? .needsSignIn : .connected,
                protocolVersion: "2025-03-26",
                capabilities: ["tools"],
                toolCount: 2,
                lastSuccessAt: input.auth == .oauth ? nil : "Just now",
                credential: input.auth == .oauth ? "missing" : "present"
            ),
            auth: input.auth
        )
        mcpServers.append(server)
        let slug = input.name.lowercased().replacingOccurrences(of: " ", with: "_")
        mcpTools[id] = [
            MCPServerTool(name: "mcp_\(slug)__search", title: "Search \(input.name)", exposure: input.exposure.default ?? .discoverable),
            MCPServerTool(name: "mcp_\(slug)__get", title: "Read item", exposure: .discoverable)
        ]
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: server)
    }

    public func performMCPAction(
        serverID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        guard let idx = mcpServers.firstIndex(where: { $0.id == serverID }) else {
            throw NoriAPIError(message: "MCP server not found.", statusCode: 404, code: "not_found")
        }
        var server = mcpServers[idx]
        server.revision += 1
        switch action {
        case "sign_in", "connect", "reconnect":
            server.observed.status = .connected
            server.observed.error = nil
            server.observed.credential = "present"
            server.observed.lastSuccessAt = "Just now"
            if var tools = mcpTools[serverID] {
                for i in tools.indices where tools[i].availability == .needsConnection {
                    tools[i].availability = .ready
                }
                mcpTools[serverID] = tools
            }
        case "sign_out", "stop":
            server.observed.status = .disconnected
        default:
            break
        }
        mcpServers[idx] = server
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: server)
    }

    public func setMCPToolExposure(
        serverID: String,
        expectedRevision: Int,
        toolName: String,
        exposure: MCPToolExposure,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        guard let idx = mcpServers.firstIndex(where: { $0.id == serverID }) else {
            throw NoriAPIError(message: "MCP server not found.", statusCode: 404, code: "not_found")
        }
        var server = mcpServers[idx]
        server.revision += 1
        var config = server.exposure ?? MCPExposureConfig()
        var overrides = config.overrides ?? [:]
        overrides[toolName] = exposure
        config.overrides = overrides
        server.exposure = config
        mcpServers[idx] = server

        if var tools = mcpTools[serverID], let tIdx = tools.firstIndex(where: { $0.name == toolName }) {
            tools[tIdx].exposure = exposure
            tools[tIdx].availability = exposure == .disabled ? .disabled : .ready
            mcpTools[serverID] = tools
        }
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: server)
    }

    public func fetchConnections() async throws -> [Connection] {
        connections
    }

    public func performConnectionAction(
        connectionID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<Connection> {
        guard let idx = connections.firstIndex(where: { $0.id == connectionID }) else {
            throw NoriAPIError(message: "Connection not found.", statusCode: 404, code: "not_found")
        }
        var conn = connections[idx]
        conn.revision += 1
        switch action {
        case "connect", "repair", "enable":
            conn.status = .connected
            conn.health.code = "ready"
            conn.health.message = "Connected just now"
        case "disconnect":
            conn.status = .disconnected
        case "disable":
            conn.status = .disabled
        default:
            break
        }
        connections[idx] = conn
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: conn)
    }

    // MARK: - Library & Search

    public func fetchLibrary() async throws -> [LibraryItem] {
        libraryItems
    }

    public func fetchLibraryItem(id: String) async throws -> LibraryItem {
        guard let item = libraryItems.first(where: { $0.id == id }) else {
            throw NoriAPIError(message: "Library item not found.", statusCode: 404, code: "not_found")
        }
        return item
    }

    public func search(query: String) async throws -> SearchResponse {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !q.isEmpty else { return SearchResponse(items: [], query: query) }
        var hits: [SearchHit] = []

        for item in libraryItems {
            let haystack = "\(item.title) \(item.previewText ?? "") \(item.bodyText ?? "") \(item.collection ?? "")".lowercased()
            if haystack.contains(q) {
                hits.append(SearchHit(
                    kind: item.kind == .file ? .file : .result,
                    id: item.id,
                    title: item.title,
                    snippet: item.previewText ?? item.collection ?? "",
                    jump: SearchJump(kind: "library", itemID: item.id),
                    provenance: item.provenance,
                    freshness: item.createdAt
                ))
            }
        }
        for msg in session.messages where msg.text.lowercased().contains(q) {
            hits.append(SearchHit(
                kind: .message,
                id: msg.id,
                title: msg.isFromUser ? "You in Chat" : "Nori in Chat",
                snippet: msg.text,
                jump: SearchJump(kind: "conversation", conversationID: "assistant", messageID: msg.id),
                freshness: msg.createdAt
            ))
        }
        for task in workItems where "\(task.title) \(task.progress ?? "")".lowercased().contains(q) {
            hits.append(SearchHit(
                kind: .task,
                id: task.taskID,
                title: task.title,
                snippet: task.progress ?? task.status.rawValue,
                jump: SearchJump(kind: "task", taskID: task.taskID),
                freshness: task.startedAt
            ))
        }
        for mem in coreMemories where "\(mem.text) \(mem.section ?? "")".lowercased().contains(q) {
            hits.append(SearchHit(
                kind: .memory,
                id: mem.id,
                title: mem.section ?? "Core Memory",
                snippet: mem.text,
                jump: SearchJump(kind: "memory", layer: "core", memoryID: mem.id),
                freshness: mem.createdAt
            ))
        }
        for goal in goals where "\(goal.title) \(goal.outcome)".lowercased().contains(q) {
            hits.append(SearchHit(
                kind: .goal,
                id: goal.id,
                title: goal.title,
                snippet: goal.progress,
                jump: SearchJump(kind: "goal", goalID: goal.id),
                freshness: goal.deadline
            ))
        }
        return SearchResponse(items: hits, query: query)
    }

    public func reviseLibraryItem(
        id: String,
        expectedRevision: Int,
        instruction: String,
        requestID: String
    ) async throws -> MutationReceipt<LibraryItem> {
        guard let idx = libraryItems.firstIndex(where: { $0.id == id }) else {
            throw NoriAPIError(message: "Document not found.", statusCode: 404, code: "not_found")
        }
        var item = libraryItems[idx]
        item.version += 1
        item.revision += 1
        item.updatedAt = "Just now"
        let note = "\n\n---\n**Revision v\(item.version) note:** Applied \"\(instruction)\"."
        item.bodyText = (item.bodyText ?? item.previewText ?? "") + note
        libraryItems[idx] = item
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: item)
    }

    // MARK: - Memory

    public func fetchCoreMemories() async throws -> [MemoryFact] {
        coreMemories
    }

    public func fetchMemoryLogs() async throws -> [MemoryLog] {
        memoryLogs
    }

    public func fetchWorkingMemory() async throws -> WorkingMemory {
        workingMemory
    }

    public func createMemory(
        text: String,
        section: String?,
        about: String?,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryFact> {
        let fact = MemoryFact(
            id: "mem-\(UUID().uuidString.prefix(6).lowercased())",
            revision: 1,
            text: text,
            about: about,
            section: section ?? "Preferences",
            pinned: pinned,
            provenance: .userExplicit,
            createdAt: "Just now"
        )
        coreMemories.insert(fact, at: 0)
        workingMemory.coreBytes = min(workingMemory.coreBudgetBytes, workingMemory.coreBytes + max(180, text.utf8.count * 2))
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: fact)
    }

    public func performMemoryAction(
        id: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<MemoryFact> {
        guard let idx = coreMemories.firstIndex(where: { $0.id == id }) else {
            throw NoriAPIError(message: "Memory not found.", statusCode: 404, code: "not_found")
        }
        var fact = coreMemories[idx]
        fact.revision += 1
        switch action {
        case "pin":
            fact.pinned = true
            coreMemories[idx] = fact
        case "unpin":
            fact.pinned = false
            coreMemories[idx] = fact
        case "forget", "demote":
            coreMemories.remove(at: idx)
            workingMemory.coreBytes = max(0, workingMemory.coreBytes - max(180, fact.text.utf8.count * 2))
            if action == "demote" {
                let demoted = MemoryLogEntry(id: "log-\(UUID().uuidString.prefix(6).lowercased())", section: fact.section, text: fact.text, provenance: fact.provenance ?? .userExplicit, createdAt: "Just now", promoted: false)
                if memoryLogs.isEmpty {
                    memoryLogs.append(MemoryLog(date: "2026-11-23", entries: [demoted], updatedAt: "Just now"))
                } else {
                    memoryLogs[0].entries.insert(demoted, at: 0)
                }
            }
        default:
            coreMemories[idx] = fact
        }
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: fact)
    }

    public func performMemoryLogEntryAction(
        date: String,
        entryID: String,
        expectedRevision: Int,
        action: String,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryLog> {
        guard let logIdx = memoryLogs.firstIndex(where: { $0.date == date }),
              let entryIdx = memoryLogs[logIdx].entries.firstIndex(where: { $0.id == entryID }) else {
            throw NoriAPIError(message: "Log entry not found.", statusCode: 404, code: "not_found")
        }
        let entry = memoryLogs[logIdx].entries[entryIdx]
        memoryLogs[logIdx].revision += 1
        if action == "promote" {
            memoryLogs[logIdx].entries[entryIdx].promoted = true
            let promoted = MemoryFact(
                id: "mem-prom-\(UUID().uuidString.prefix(5).lowercased())",
                revision: 1,
                text: entry.text,
                about: entry.about,
                section: entry.section ?? "Context",
                pinned: pinned,
                provenance: entry.provenance,
                createdAt: "Just now"
            )
            coreMemories.insert(promoted, at: 0)
            workingMemory.coreBytes = min(workingMemory.coreBudgetBytes, workingMemory.coreBytes + max(180, entry.text.utf8.count * 2))
        } else if action == "forget" {
            memoryLogs[logIdx].entries.remove(at: entryIdx)
        }
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: memoryLogs[logIdx])
    }

    public func performWorkingMemoryAction(
        itemID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<WorkingMemory> {
        guard let idx = workingMemory.items.firstIndex(where: { $0.id == itemID }) else {
            throw NoriAPIError(message: "Working memory item not found.", statusCode: 404, code: "not_found")
        }
        let item = workingMemory.items.remove(at: idx)
        workingMemory.revision += 1
        if action == "promote" {
            let fact = MemoryFact(
                id: "mem-wm-\(UUID().uuidString.prefix(5).lowercased())",
                revision: 1,
                text: item.quote,
                section: "Context",
                pinned: false,
                provenance: .userExplicit,
                createdAt: "Just now"
            )
            coreMemories.insert(fact, at: 0)
            workingMemory.coreBytes = min(workingMemory.coreBudgetBytes, workingMemory.coreBytes + max(180, item.quote.utf8.count * 2))
        }
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: workingMemory)
    }

    // MARK: - Automations, Goals, Permissions, Preferences

    public func fetchAutomations() async throws -> [Automation] {
        automations
    }

    public func performAutomationAction(
        id: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<Automation> {
        guard let idx = automations.firstIndex(where: { $0.id == id }) else {
            throw NoriAPIError(message: "Automation not found.", statusCode: 404, code: "not_found")
        }
        var auto = automations[idx]
        auto.revision += 1
        switch action {
        case "pause":
            auto.status = .paused
        case "resume":
            auto.status = .enabled
        case "cancel":
            auto.status = .cancelled
        case "run_now":
            let runID = "task-auto-\(UUID().uuidString.prefix(5).lowercased())"
            let task = WorkItem(
                taskID: runID,
                title: auto.title,
                status: .running,
                bucket: .working,
                origin: WorkOrigin(kind: .automation, automationID: auto.id),
                progress: "Running manual trigger now…",
                cost: WorkCost(amountUSD: 0.01, status: .known, budgetUSD: auto.budgetUSD),
                startedAt: "Just now"
            )
            workItems.insert(task, at: 0)
            workDetails[runID] = WorkDetail(
                item: task,
                events: [RunEvent(kind: "accepted", text: "Manually triggered \"\(auto.title)\".", createdAt: Self.shortTimeString())]
            )
            auto.lastOccurrence = AutomationLastOccurrence(id: "occ-\(runID)", status: "running", scheduledAt: "Just now", summary: "Triggered manually")
        default:
            break
        }
        automations[idx] = auto
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: auto)
    }

    public func fetchGoals() async throws -> [Goal] {
        goals
    }

    public func fetchPermissions() async throws -> [PermissionRule] {
        permissions
    }

    public func fetchPreferences() async throws -> Preferences {
        preferences
    }

    public func savePreferences(_ preferences: Preferences, requestID: String) async throws -> MutationReceipt<Preferences> {
        var updated = preferences
        updated.revision += 1
        updated.updatedAt = Self.shortTimeString()
        self.preferences = updated
        return MutationReceipt(requestID: requestID, accepted: true, status: .applied, resource: updated)
    }

    // MARK: - Formatting & Canned Replies

    private static func shortTimeString() -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "h:mm a"
        return formatter.string(from: Date())
    }

    private static func demoReply(for text: String) -> String {
        let lower = text.lowercased()
        if lower.contains("budget") || lower.contains("renewal") || lower.contains("datadog") {
            return "Trimming the 2 unused Datadog seats brings Q4 renewals from **$48,200** down to **$44,600** — right under your $45k cap. Want me to revise the brief or hold the procurement email?"
        }
        if lower.contains("calendar") || lower.contains("friday") || lower.contains("meeting") {
            return "Friday 2:00–2:45 PM PT is clear for all 4 attendees. Confirm your calendar choice in **Work → Needs you** and I'll send the hold right away."
        }
        if lower.contains("linear") || lower.contains("blocker") {
            return "Linear Workspace needs a quick OAuth sign-in before I can pull fresh sprint issues — tap **Apps → Linear Workspace** to reconnect."
        }
        return "Got it — I've noted \"\(text)\" and checked your active tasks and memory. You still have pending items in **Work → Needs you** whenever you're ready to review them."
    }
}
