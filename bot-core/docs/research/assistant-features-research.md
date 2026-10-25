# Core assistant features

Research date: October 24, 2026.

The product direction is a persistent personal assistant in the style of Grok Bot, Muse, and dots: it understands the user's context, owns work over time, uses tools and a computer, and brings decisions and results back to one place. The capabilities below describe the direction under discussion; technical approaches remain options for the new implementation.

## Functional capabilities

### A persistent primary conversation

The main thread is the control plane for chat, delegation, scheduled work, progress checks, approvals, and deliverables. The user should be able to ask about prior work without finding the original message or selecting the correct model or context window. Expandable task details and focused side conversations can keep long work readable.

Muse explicitly describes an ongoing main conversation with optional side chats. Grok Bot supports persistent named specialists. Dots presents a primary assistant that can handle several projects while the user keeps bringing it work. [Muse design](https://introducing.muse.ai/), [Grok Bot guide](https://x.ai/bot/guides/grok-bot-101), [Introducing dots](https://openai.com/index/introducing-dots/)

### Ownership of ongoing work

Track requested outcomes, deadlines, next steps, and blockers. Distinguish running work, a pending user decision, an external dependency, a failed attempt, and confirmed completion. Accept new messages and steering while existing work continues. Continue authorized background work when the client is closed.

Long-term goals can be broken into concrete tasks, but the assistant's proposed plans remain proposals until the user adopts them.

### Useful and correctable memory

Remember stable preferences, relationships, recurring constraints, important decisions, and relevant history. Keep explicit user statements distinct from inferences, hypothetical examples, and assistant suggestions. Retain sources and unresolved conflicts where they affect later decisions. Support inspection, correction, and forgetting.

Memory controls vary across products. Muse describes editable memory files; dots currently says individual memories cannot be directly viewed, edited, or deleted. [Muse design](https://introducing.muse.ai/), [Dots memory FAQ](https://help.openai.com/en/articles/20001529-dots-privacy-security-and-safety-faqs)

### Proactivity with judgment

Look for actionable changes in permitted sources, prepare useful work, and decide whether the result deserves an interruption. Support quiet periods, urgency, batching, and notification preferences. Finding something, preparing work, taking an external action, and notifying the user are separate decisions.

Examples supplied by Austin:

- Notice that a dentist appointment is close to a 2 p.m. meeting and offer to resolve the conflict.
- Surface the draft the user intended to send to Tom.

These examples describe desired behavior, not facts about Austin's actual appointments or contacts.

Dots documents a restricted proactive research path that can read permitted sources and save private notes, while its research tools cannot directly send messages, modify connected content, or operate a browser or computer. [Dots permissions](https://help.openai.com/en/articles/20001529-dots-privacy-security-and-safety-faqs)

### Scheduling and monitoring

Handle one-time reminders, recurring tasks, deadlines, and conditional follow-ups. Let the user list, change, pause, and cancel commitments. Keep schedules explicit and durable so promised future work survives the end of a conversation turn.

### Real tools and a persistent computer

Work across email, calendars, documents, websites, APIs, and CLIs. Understand which account and identity an action uses. Maintain files, browser sessions, installed tools, code, and working directories on the assistant's own computer. Support login, MFA, CAPTCHA, and user takeover when needed. [Grok Bot guide](https://x.ai/bot/guides/grok-bot-101)

### Durable deliverables and visible progress

Produce actual drafts, documents, spreadsheets, research files, websites, and code changes. Let the user preview, revise, export, and retrieve results later. Show what is happening, what changed, what needs a decision, and the evidence for completion. Distinguish prepared, attempted, submitted, and confirmed outcomes.

### Controllable autonomy and learning

Apply standing authority within its scope. Ask for concrete decisions when needed. Support steering, pause, cancellation, and computer takeover, with an explanation of actions already completed. Turn corrections into appropriately scoped preferences or reusable procedures.

### Low user overhead

Manage model selection, context retrieval, compaction, tool discovery, and execution internally. Ask about user-facing tradeoffs such as urgency, quality, or budget when those choices matter. Preserve the same assistant and work state across desktop and mobile; voice and messaging channels can extend access.

## Technical responsibilities

| Responsibility | What it supports |
| --- | --- |
| Foreground interaction | Responsive conversation, clarification, steering, and status while tasks run |
| Background execution | Longer tasks with bounded execution, checkpoints, cancellation, and resumability |
| Scheduling and events | Time-based wakeups, external events, recurrence, time zones, deduplication, and delivery rules |
| Durable task state | Outcomes, lifecycle, dependencies, blockers, approvals, execution records, and results |
| Memory | User knowledge, history, reusable procedures, sources, uncertainty, and corrections |
| Authority records | Explicit permissions and operating rules with enforceable scope |
| Context management | Relevant instructions, memory, recent conversation, task state, and selectively retrieved evidence |
| Execution environment | Persistent files and browser profiles, dependencies, resource limits, and recovery |
| Tool integration | Discoverable interfaces, account identity, permissions, clear errors, and reliable execution |
| Computer use | Observation, actions, verification, shared-session coordination, takeover, and recovery |
| Artifact management | Stable identities, storage, previews, versions, ownership, exports, and task links |
| Cost management | Spending visibility, budgets, retry limits, concurrency, caching, and idle compute management |
| Verification and activity history | Evidence of outcomes, inspectable changes, failures, approval decisions, and costs |

### Separate conversation continuity from model context

A persistent conversation needs durable records beyond the tokens supplied to one model call. Keep active work and authority in explicit state. Retrieve relevant history as needed, and compact conversation while preserving constraints, decisions, unresolved questions, and evidence references. Anthropic describes compaction, structured notes, and just-in-time retrieval as complementary techniques. [Context engineering](https://www.anthropic.com/research/effective-context-engineering-for-ai-agents)

Execution checkpoints and information retained across conversations serve different purposes. A framework can supply these capabilities, but the research does not establish a framework choice for bot. [LangGraph persistence](https://docs.langchain.com/oss/python/langgraph/persistence)

### Separate the agent lifecycle from its computer

The assistant should be able to receive events and retain task state while its computer sleeps or is replaced. Durable files, browser profiles, application state, scheduling, and compute sessions need defined lifecycles. A microVM is one possible isolation boundary. Firecracker supplies lightweight virtualization and resource controls; the application still owns lifecycle and state management. [Firecracker](https://firecracker-microvm.github.io/)

### Enforce authority across every execution path

Permissions must apply to connectors, browser operations, shell commands, and delegated work. Scope consequential actions to destinations, recipients, content, resources, and purpose. Keep credentials out of model context where practical. Untrusted websites, email, and documents cannot grant authority.

Muse publishes an architecture that separates its editable agent runtime from credential handling and a host-side permission authority. Tool annotations can inform decisions, but MCP describes them as hints rather than enforcement guarantees. [Muse security architecture](https://research.meta.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse), [MCP tool annotations](https://blog.modelcontextprotocol.io/posts/2026-03-16-tool-annotations/)

### Use tools and compute efficiently

Discover tool schemas on demand, filter large results before model ingestion, cache stable context, and bound retries, runtime, and concurrency. Coordinate shared browser and desktop access so concurrent tasks do not interfere. Prefer event-triggered checks where available, and avoid invoking a model repeatedly for unchanged state. [Efficient MCP execution](https://www.anthropic.com/research/code-execution-with-mcp), [OpenClaw automation](https://docs.openclaw.ai/automation)

### Verify outcomes before reporting completion

Check the requested result: the file opens, the draft exists, the calendar change is present, or the application works. A successful tool call alone may not establish the user-visible outcome. Long-running tasks also need useful handoffs between context windows. [Long-running agent harnesses](https://www.anthropic.com/research/effective-harnesses-for-long-running-agents)

## Questions for a future implementation

- Which real workflow should be useful first: scheduling conflicts, unfinished communications, research, or another responsibility?
- What work can run independently, and what authority needs a user decision?
- Which information belongs in memory, task state, artifacts, and permissions?
- What interruption policy makes proactive work valuable?
- Which existing infrastructure can be reused cheaply without making infrastructure the product?

Model routing, specialist agents, custom microVM hosting, and broader production infrastructure remain optional approaches. The research does not make them requirements for the restart.
