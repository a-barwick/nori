// Development-only fixture service for the assistant management API. It serves the
// contract fixtures in contracts/assistant/fixtures (plus hand-written examples for
// shapes the contract fixtures do not cover) and applies mutations in memory with the
// contract's receipt, revision and stale-interaction rules, so screens can be built
// before backend routes land. Nothing here is integration evidence.
import { readFileSync } from "node:fs";
import { join } from "node:path";

type Doc = Record<string, unknown> & { id?: string; revision?: number };
const dir = join(process.cwd(), "..", "contracts", "assistant", "fixtures");
const fixture = (file: string): Doc => JSON.parse(readFileSync(join(dir, file), "utf8"));
const now = () => new Date().toISOString();
const hours = (n: number) => new Date(Date.now() + n * 3_600_000).toISOString();

type Store = {
  collections: Record<string, Doc[]>;
  receipts: Map<string, { key: string; status: number; body: unknown }>;
  preferences: Doc;
};

function seed(): Store {
  const google = fixture("connections/connection.needs-attention.valid.json");
  const work = { ...fixture("connections/connection.connected.valid.json"), id: "conn_google_work", title: "Work Google", account: { provider_id: "acct_work", label: "you@work.example" }, defaults: [] } as Doc;
  const automations = ["recurring", "once", "event-relative", "watch"].map((name) => {
    const { authority_ref: _ref, ...rest } = fixture(`automations/automation.${name}.valid.json`);
    void _ref;
    return { ...rest, authority_rule_ids: [`rule_${rest.id}`] } as Doc;
  });
  const occurrence = fixture("automations/occurrence.valid.json");
  const delivery = fixture("automations/delivery.valid.json");
  const event = fixture("domain/calendar-event.timed.valid.json");
  const draft = fixture("domain/mail-draft.valid.json");
  const goal = fixture("automations/goal.valid.json");
  return {
    receipts: new Map(),
    preferences: {
      revision: 1, timezone: "Europe/London", locale: "en-GB",
      working_hours: [1, 2, 3, 4, 5].map((day) => ({ day, start: "09:00", end: "17:30" })),
      quiet_hours: { start: "22:00", end: "07:00" }, default_destination_ids: ["dest_inapp", "dest_push"],
      account_defaults: { "calendar.create": "conn_google_personal", "mail.send": "conn_gmail" },
      budgets: { task_usd: 2, monthly_usd: 40 }, input: { dictation_language: "en-GB" },
    },
    collections: {
      integrations: [
        { id: "google", title: "Google Calendar", description: "Agenda, availability and event changes.", categories: ["Calendar"], icon: "calendar", source: "native", auth: "oauth", multiple_accounts: true,
          purposes: [{ id: "calendar.read", title: "Read events" }, { id: "calendar.create", title: "New events" }],
          capabilities: [
            { id: "calendar.read", title: "Read events and free/busy", effect: "read", supported: true, scopes: ["calendar.readonly"] },
            { id: "calendar.write", title: "Create, move and cancel events", effect: "reversible_write", supported: true, scopes: ["calendar.events"] },
            { id: "calendar.invite", title: "Send invitations", effect: "sensitive_send", supported: true, scopes: ["calendar.events"] },
          ], availability: "available", connection_ids: [google.id, work.id] },
        { id: "gmail", title: "Gmail", description: "Search, read, draft and send mail.", categories: ["Email"], icon: "mail", source: "native", auth: "oauth", multiple_accounts: true,
          purposes: [{ id: "mail.send", title: "Send as" }],
          capabilities: [
            { id: "mail.read", title: "Search and read mail", effect: "read", supported: true },
            { id: "mail.draft", title: "Write drafts", effect: "reversible_write", supported: true },
            { id: "mail.send", title: "Send mail", effect: "sensitive_send", supported: true },
            { id: "mail.labels", title: "Labels and archive", effect: "reversible_write", supported: true },
          ], availability: "available", connection_ids: ["conn_gmail"] },
        { id: "outlook", title: "Outlook", description: "Microsoft 365 calendar and mail.", categories: ["Calendar", "Email"], icon: "mail", source: "native", auth: "oauth", multiple_accounts: true, purposes: [],
          capabilities: [{ id: "calendar.read", title: "Read events", effect: "read", supported: true }, { id: "mail.read", title: "Read mail", effect: "read", supported: true }],
          availability: "blocked", availability_reason: "Nori needs a Microsoft app registration before anyone can connect Outlook.", connection_ids: [] },
        { id: "icloud", title: "iCloud Calendar", description: "Apple calendars with an app-specific password.", categories: ["Calendar"], icon: "calendar", source: "native", auth: "app_password", multiple_accounts: true, purposes: [],
          capabilities: [{ id: "calendar.read", title: "Read events", effect: "read", supported: true }, { id: "calendar.write", title: "Change events", effect: "reversible_write", supported: true }, { id: "calendar.invite", title: "Send invitations", effect: "sensitive_send", supported: false, reason: "iCloud does not send invitations for third-party apps." }],
          availability: "available", connection_ids: [] },
        { id: "hey", title: "HEY", description: "Your HEY inbox.", categories: ["Email"], icon: "mail", source: "native", auth: "api_key", multiple_accounts: false, purposes: [],
          capabilities: [{ id: "mail.read", title: "Read mail", effect: "read", supported: true }, { id: "mail.send", title: "Send mail", effect: "sensitive_send", supported: false, reason: "No supported send path yet." }],
          availability: "unavailable", availability_reason: "HEY access is still being worked out.", connection_ids: [] },
        { id: "linear", title: "Linear", description: "Issues and projects.", categories: ["Projects"], icon: "list", source: "mcp", installation_id: "inst_1", auth: "oauth", multiple_accounts: false, purposes: [],
          capabilities: [{ id: "issues.read", title: "Search issues", effect: "read", supported: true }, { id: "issues.write", title: "Create and update issues", effect: "reversible_write", supported: true }],
          availability: "available", connection_ids: ["conn_linear"] },
        { id: "drive", title: "Google Drive", description: "Find and read documents.", categories: ["Files"], icon: "folder", source: "native", auth: "oauth", multiple_accounts: true, purposes: [],
          capabilities: [{ id: "files.read", title: "Find and read files", effect: "read", supported: true }], availability: "available", connection_ids: [] },
        { id: "slack", title: "Slack", description: "Messages and channels.", categories: ["Messaging"], icon: "message", source: "plugin", auth: "oauth", multiple_accounts: true, purposes: [],
          capabilities: [{ id: "messages.read", title: "Read channels", effect: "read", supported: true }, { id: "messages.send", title: "Post messages", effect: "sensitive_send", supported: true }],
          availability: "blocked", availability_reason: "Nori needs a Slack app before anyone can connect Slack.", connection_ids: [] },
      ],
      connections: [
        { ...google, health: { ...(google.health as object), code: "needs_sign_in", recovery: "repair" }, capabilities: ["calendar.read", "calendar.write"] },
        { ...work, health: { ...(work.health as object ?? {}), code: "ready", recovery: "none", last_success_at: hours(-1) }, capabilities: ["calendar.read"] },
        { id: "conn_gmail", revision: 2, integration_id: "gmail", title: "Personal Gmail", account: { provider_id: "acct_123", label: "personal@example.com" }, status: "connected",
          granted_scopes: ["gmail.readonly", "gmail.compose"], selected_resource_ids: ["INBOX"], health: { code: "needs_more_access", recovery: "request_access", message: "Sending needs one more permission.", last_success_at: hours(-3) },
          missing_scopes: ["gmail.send"], defaults: ["mail.send"], capabilities: ["mail.read", "mail.draft"] },
        { id: "conn_linear", revision: 1, integration_id: "linear", title: "Linear", account: { provider_id: "ws_1", label: "Acme workspace" }, status: "connected", granted_scopes: ["read", "write"], selected_resource_ids: [], health: { code: "ready", recovery: "none", last_success_at: hours(-20) }, defaults: [] },
      ],
      resources: [
        { ref: { connection_id: google.id, kind: "calendar", provider_id: "primary" }, title: "Personal", kind: "calendar", selected: true, writable: true },
        { ref: { connection_id: google.id, kind: "calendar", provider_id: "family" }, title: "Family", kind: "calendar", selected: true, writable: true },
        { ref: { connection_id: google.id, kind: "calendar", provider_id: "holidays" }, title: "UK holidays", kind: "calendar", selected: false, writable: false },
        { ref: { connection_id: work.id, kind: "calendar", provider_id: "work" }, title: "Work", kind: "calendar", selected: true, writable: true },
        { ref: { connection_id: "conn_gmail", kind: "sender", provider_id: "personal@example.com" }, title: "personal@example.com", kind: "sender", selected: true, writable: true },
      ],
      interactions: [
        { ...fixture("permissions/interaction.approval.valid.json"), task_title: "Book the dentist and invite Alex", expires_at: hours(2) },
        { ...fixture("permissions/interaction.question.valid.json"), task_title: "Book the dentist and invite Alex" },
        { ...fixture("permissions/interaction.account-selection.valid.json"), status: "resolved", resolution: { kind: "choose_account", connection_id: google.id, at: hours(-1) }, task_title: "Book the dentist and invite Alex" },
        { ...fixture("permissions/interaction.sign-in.valid.json"), task_id: "task_12", task_title: "Check the school portal for this term's dates", expires_at: hours(1) },
      ],
      work: [
        { task_id: "task_9", title: "Book the dentist and invite Alex", status: "running", bucket: "needs_you", instruction_revision: 3, origin: { kind: "conversation", conversation_id: "assistant" }, interaction_ids: ["int_5", "int_1"], progress: "Found a 10:00 slot on Tuesday.", operations: { total: 3, uncertain: 0, failed: 0, submitted: 0 }, result_ids: [], cost: { status: "known", amount_usd: 0.12, budget_usd: 2 }, started_at: hours(-1), cancel_requested: false },
        { task_id: "task_12", title: "Check the school portal for this term's dates", status: "running", bucket: "needs_you", instruction_revision: 1, origin: { kind: "conversation", conversation_id: "assistant" }, interaction_ids: ["int_7"], progress: "Waiting for you to sign in on the computer.", operations: { total: 2, uncertain: 0, failed: 0, submitted: 0 }, result_ids: [], cost: { status: "known", amount_usd: 0.05, budget_usd: 2 }, started_at: hours(-0.5), cancel_requested: false },
        { task_id: "task_14", title: "Compare three standing desks under £400", status: "running", bucket: "working", instruction_revision: 2, origin: { kind: "conversation", conversation_id: "assistant" }, interaction_ids: [], progress: "Reading reviews for the second desk.", operations: { total: 7, uncertain: 0, failed: 0, submitted: 0 }, result_ids: [], cost: { status: "known", amount_usd: 0.31, budget_usd: 2 }, started_at: hours(-0.3), cancel_requested: false },
        { task_id: "task_15", title: "Reply to the landlord about the boiler visit", status: "running", bucket: "working", instruction_revision: 1, origin: { kind: "conversation", conversation_id: "assistant" }, interaction_ids: [], progress: "Sending the reply.", operations: { total: 2, uncertain: 1, failed: 0, submitted: 0 }, result_ids: [], cost: { status: "unknown" }, started_at: hours(-0.2), cancel_requested: false },
        { task_id: "task_20", title: "Morning briefing", status: "completed", bucket: "finished", instruction_revision: 1, origin: { kind: "automation", automation_id: "auto_1", occurrence_id: occurrence.id as string }, interaction_ids: [], operations: { total: 4, uncertain: 0, failed: 0, submitted: 0 }, result_ids: ["res_7"], cost: { status: "known", amount_usd: 0.08 }, started_at: hours(-26), finished_at: hours(-25.9), cancel_requested: false },
        { task_id: "task_4", title: "Laptop comparison", status: "completed", bucket: "finished", instruction_revision: 2, origin: { kind: "conversation", conversation_id: "assistant" }, interaction_ids: [], operations: { total: 11, uncertain: 0, failed: 0, submitted: 0 }, result_ids: ["res_3"], cost: { status: "known", amount_usd: 0.44 }, started_at: hours(-50), finished_at: hours(-49.6), cancel_requested: false },
        { task_id: "task_3", title: "Find flights to Lisbon in March", status: "failed", bucket: "finished", instruction_revision: 1, origin: { kind: "conversation", conversation_id: "assistant" }, interaction_ids: [], operations: { total: 5, uncertain: 0, failed: 1, submitted: 0 }, result_ids: [], cost: { status: "known", amount_usd: 0.2 }, started_at: hours(-70), finished_at: hours(-69.8), cancel_requested: false, error: { code: "provider_error", message: "The airline site stopped responding. Nothing was booked.", retryable: true } },
      ],
      operations: [
        { ...fixture("execution/operation-projection.valid.json"), task_title: "Book the dentist and invite Alex", recovery: "none", created_at: hours(-0.8) },
        { id: "op_51", task_id: "task_15", instruction_revision: 1, name: "gmail_send", effect_class: "sensitive_send", outcome: "uncertain", connection_id: "conn_gmail", account_summary: "Personal Gmail (personal@example.com)", action_summary: "Reply to Sam Patel: boiler visit Thursday 9–11", authority_source: "user_request", task_title: "Reply to the landlord about the boiler visit", recovery: "reconciling", created_at: hours(-0.1) },
        { id: "op_30", task_id: "task_20", instruction_revision: 1, name: "calendar_list_events", effect_class: "read", outcome: "confirmed", connection_id: work.id, account_summary: `Work (${(work.account as { label: string }).label})`, action_summary: "Read today's events on Work", authority_source: "automation", task_title: "Morning briefing", recovery: "none", created_at: hours(-26) },
      ],
      automations,
      occurrences: [
        { ...occurrence, deliveries: [delivery, { ...delivery, id: "dlv_2", destination_id: "dest_inapp", state: "delivered", acknowledged_at: hours(-25) }] },
        { id: "auto_1:2026-11-09T07:30:00Z:g2", automation_id: "auto_1", automation_revision: 3, generation: 2, scheduled_at: hours(-50), status: "skipped", reason: "missed_window", task_ids: [], delivery_ids: [] },
        { id: "auto_1:2026-11-08T07:30:00Z:g2", automation_id: "auto_1", automation_revision: 3, generation: 2, scheduled_at: hours(-74), status: "failed", task_ids: ["task_18"], delivery_ids: [], summary: "Calendar access needed sign-in.", error: { code: "needs_sign_in", message: "Personal Google needs sign-in.", retryable: false } },
      ],
      permissions: [
        { ...fixture("permissions/rule.allow-calendar.valid.json"), summary: "Create and change events on Personal (primary) without asking." },
        { ...fixture("permissions/rule.automation-send.valid.json"), summary: "Morning briefing may send its summary to you by push." },
        { id: "rule_auto_1", revision: 1, effect: "allow", tool_ids: ["nori.google/calendar/events.list", "nori.gmail/messages.search"], connection_ids: [google.id, "conn_gmail"], resource_ids: ["primary"], action_classes: ["read"], automation_id: "auto_1", title: "Morning briefing reads", summary: "Read Personal calendar and search Personal Gmail each weekday morning." },
        { id: "rule_deny", revision: 1, effect: "deny", tool_ids: ["*"], connection_ids: [work.id], resource_ids: ["*"], action_classes: ["sensitive_send"], title: "Never send from work", summary: "Never send invitations or mail from the Work account." },
      ],
      goals: [
        { ...goal, linked: { task_ids: ["task_9"], automation_ids: ["auto_1"], result_ids: [] }, blockers: [{ text: "Smile Clinic has not replied", interaction_id: "int_1" }] },
        { id: "goal_2", revision: 1, title: "Renew passport", outcome: "New passport in hand before the March trip.", completion_criteria: [{ id: "c1", text: "Application submitted", met: true, evidence_result_ids: ["res_8"] }, { id: "c2", text: "Passport received", met: false }], status: "active", progress: "Application submitted 2 Nov.", next_actions: ["Check tracking on 16 Nov"], blockers: [], linked: { task_ids: [], automation_ids: [], result_ids: ["res_8"] }, deadline: "2027-02-15T00:00:00Z", follow_up: "remind", created_at: hours(-200) },
        { id: "goal_3", revision: 3, title: "Clear the garage", outcome: "Garage cleared and old bikes sold.", completion_criteria: [{ id: "c1", text: "Bikes listed", met: true }, { id: "c2", text: "Tip run booked", met: true }], status: "completed", progress: "Done.", next_actions: [], blockers: [], linked: { task_ids: [], automation_ids: [], result_ids: [] }, follow_up: "none", completed_at: hours(-300), created_at: hours(-900) },
      ],
      library: [
        fixture("library/library-item.copied.valid.json"),
        { ...fixture("library/library-item.copied-from-disconnected.valid.json"), preview_text: "Booking ref LX4421 · Hotel Alfama · 14–18 March", delete_effect: "Removes this saved copy from Nori. The original email stays in Gmail." },
        { id: "res_3", root_id: "res_3", version: 3, revises: "res_3v2", kind: "report", title: "Laptop comparison", media_type: "text/markdown", task_id: "task_4", conversation_id: "assistant", provenance: { content: "generated", retrieved_at: hours(-49.6) }, index_status: "indexed", preview_text: "Framework 13 wins on repairability; the MacBook Air on battery.", collection: "Shopping", created_at: hours(-49.6) },
        { id: "res_1", root_id: "res_1", version: 1, kind: "event", title: "Dentist", task_id: "task_9", provenance: { content: "live", source: event.ref, retrieved_at: hours(-0.8), connection_status: "connected" }, data_kind: "calendar_event", data: event, created_at: hours(-0.8) },
        { id: "res_9", root_id: "res_9", version: 1, kind: "message", title: (draft.subject as string) ?? "Draft", task_id: "task_15", provenance: { content: "generated" }, data_kind: "mail_draft", data: draft, created_at: hours(-0.2) },
        { id: "res_10", root_id: "res_10", version: 1, kind: "file", title: "Term dates 2026–27.pdf", name: "term-dates.pdf", media_type: "application/pdf", size: 182_000, provenance: { content: "uploaded" }, collection: "School", created_at: hours(-120) },
      ],
      destinations: [
        { id: "dest_inapp", revision: 1, channel: "in_app", label: "Nori inbox", status: "verified", supports_receipts: true },
        fixture("automations/destination.valid.json"),
        { id: "dest_email", revision: 1, channel: "email", label: "personal@example.com", status: "pending_verification", supports_receipts: false },
      ],
      installations: [fixture("plugins/installation.valid.json")],
      catalog: [
        { package: { id: "acme/linear", version: "0.5.0", digest: "sha256:pkg3" }, title: "Linear", description: "Search and update Linear issues.", publisher: { id: "acme", name: "Acme" }, categories: ["Projects"], latest_version: "0.5.0", provenance: { source: "registry", verified: true }, requested_access: { connection_types: ["linear"], action_classes: ["read", "reversible_write"] }, contributions_summary: { tools: 24, skills: 1, prompts: 1, mcp_servers: 1 }, installation_id: "inst_1" },
        { package: { id: "nori/trip-planner", version: "1.2.0", digest: "sha256:tp" }, title: "Trip planner", description: "A skill that turns booking emails into an itinerary.", publisher: { id: "nori", name: "Nori" }, categories: ["Travel", "Skills"], latest_version: "1.2.0", provenance: { source: "registry", verified: true }, requested_access: { connection_types: [], action_classes: [] }, contributions_summary: { tools: 0, skills: 1, prompts: 0, mcp_servers: 0 } },
        { package: { id: "community/notion", version: "0.9.0", digest: "sha256:no" }, title: "Notion", description: "Read and update Notion pages through Notion's MCP server.", publisher: { id: "community", name: "Community" }, categories: ["Files", "Projects"], latest_version: "0.9.0", provenance: { source: "repository", verified: false }, requested_access: { connection_types: ["notion"], action_classes: ["read", "reversible_write"], network: ["mcp.notion.com"] }, contributions_summary: { tools: 12, skills: 0, prompts: 0, mcp_servers: 1 } },
      ],
      mcp: [fixture("plugins/mcp-server.valid.json"), { id: "mcp_2", revision: 1, source: { kind: "user" }, name: "home-notes", transport: "stdio", enabled: true, exposure: { default: "discoverable" }, observed: { status: "failed", error: { code: "process_exit", message: "The process exited with code 1 during start.", retryable: true }, process_id: "proc_2" } }],
      computer: [{ ...fixture("plugins/computer-session.valid.json"), task_title: "Check the school portal for this term's dates", waiting_task_ids: ["task_14"], lease: { id: "lease_3", holder: "user", expires_at: hours(0.25) } }],
      memory: [
        { id: "mem_1", revision: 1, text: "The user's dentist is Smile Clinic on High St.", scope: "user", origin: { conversation_id: "assistant", message_id: "msg_12" }, created_at: hours(-300), updated_at: hours(-300) },
        { id: "mem_2", revision: 2, text: "Morning briefings should lead with the first meeting.", scope: "user", origin: { conversation_id: "assistant" }, created_at: hours(-200), updated_at: hours(-20) },
      ],
      inbox: [
        { id: "in_1", delivery_id: "dlv_2", title: "Morning briefing", body: "3 meetings, 1 urgent email.", link: { kind: "result", id: "res_7" }, created_at: hours(-25.9), acknowledged_at: hours(-25) },
        { id: "in_2", title: "Nori needs your OK", body: "Send the dentist invite to Alex?", link: { kind: "interaction", id: "int_5" }, created_at: hours(-0.7) },
      ],
      skills: [fixture("plugins/skill.valid.json")],
    },
  };
}

const globalStore = globalThis as unknown as { __noriFixtures?: Store };
const db = () => (globalStore.__noriFixtures ??= seed());
export function resetFixtures() { globalStore.__noriFixtures = seed(); }

const json = (body: unknown, status = 200) => Response.json(body, { status, headers: { "Cache-Control": "no-store" } });
const error = (status: number, code: string, message: string, extra: Record<string, unknown> = {}) => json({ error: message, code, retryable: status >= 500 || status === 429, accepted: false, ...extra }, status);

const ROUTES: Record<string, { collection: string; id: (doc: Doc) => string }> = {
  integrations: { collection: "integrations", id: (doc) => doc.id as string },
  connections: { collection: "connections", id: (doc) => doc.id as string },
  interactions: { collection: "interactions", id: (doc) => doc.id as string },
  work: { collection: "work", id: (doc) => doc.task_id as string },
  automations: { collection: "automations", id: (doc) => doc.id as string },
  goals: { collection: "goals", id: (doc) => doc.id as string },
  library: { collection: "library", id: (doc) => doc.id as string },
  permissions: { collection: "permissions", id: (doc) => doc.id as string },
  "delivery-destinations": { collection: "destinations", id: (doc) => doc.id as string },
  operations: { collection: "operations", id: (doc) => doc.id as string },
  memory: { collection: "memory", id: (doc) => doc.id as string },
  inbox: { collection: "inbox", id: (doc) => doc.id as string },
  skills: { collection: "skills", id: (doc) => ((doc.ref as Doc)?.id as string) },
};

function summary() {
  const { collections } = db();
  const pending = collections.interactions.filter((item) => item.status === "pending");
  const next = collections.automations.filter((item) => item.status === "enabled" && item.next_at).map((item) => item.next_at as string).sort()[0];
  return {
    revision: 1,
    needs_you: new Set(pending.map((item) => item.task_id ?? item.id)).size,
    working: collections.work.filter((item) => item.bucket === "working").length,
    apps_attention: collections.connections.filter((item) => (item.health as Doc)?.code && (item.health as Doc).code !== "ready").length,
    inbox_unread: collections.inbox.filter((item) => !item.acknowledged_at).length,
    next_scheduled_at: next,
  };
}

function list(items: Doc[], url: URL, filter?: (doc: Doc) => boolean) {
  const q = url.searchParams.get("q")?.toLowerCase();
  let out = filter ? items.filter(filter) : items;
  if (q) out = out.filter((doc) => JSON.stringify(doc).toLowerCase().includes(q));
  return json({ items: out });
}

function search(url: URL) {
  const q = (url.searchParams.get("q") ?? "").toLowerCase().trim();
  const { collections } = db();
  if (!q) return json({ items: [], query: q });
  const hits: Doc[] = [];
  const match = (text: unknown) => typeof text === "string" && text.toLowerCase().includes(q);
  const snippet = (text: string) => {
    const at = text.toLowerCase().indexOf(q);
    return (at > 40 ? "…" : "") + text.slice(Math.max(0, at - 40), at + q.length + 80);
  };
  for (const item of collections.library) if (match(item.title) || match(item.preview_text)) hits.push({ kind: "result", id: item.id!, title: item.title, snippet: snippet(`${item.preview_text ?? item.title}`), jump: { kind: "library", item_id: item.id }, provenance: item.provenance, freshness: item.created_at });
  for (const item of collections.work) if (match(item.title) || match(item.progress)) hits.push({ kind: "task", id: item.task_id as string, title: item.title, snippet: snippet(`${item.progress ?? item.title}`), jump: { kind: "task", task_id: item.task_id } });
  for (const item of collections.goals) if (match(item.title) || match(item.outcome)) hits.push({ kind: "goal", id: item.id!, title: item.title, snippet: snippet(`${item.outcome}`), jump: { kind: "goal", goal_id: item.id } });
  for (const item of collections.automations) if (match(item.title) || match(item.instruction)) hits.push({ kind: "automation", id: item.id!, title: item.title, snippet: snippet(`${item.instruction}`), jump: { kind: "automation", automation_id: item.id } });
  if ("laptop comparison".includes(q) || q.includes("comparison")) hits.push({ kind: "message", id: "msg_55", title: "Nori in chat", snippet: "…the Framework 13 wins on repairability, the Air on battery…", jump: { kind: "conversation", conversation_id: "assistant", message_id: "msg_55" } });
  return json({ items: hits, query: q });
}

export async function fixtureGET(segments: string[], url: URL) {
  const { collections, preferences } = db();
  const [head, id, sub] = segments;
  if (head === "capabilities") return json({ contract: "nori-chat-2", conversationContract: "nori-conversations-1", assistant_contract: "nori-assistant-1", assistant_features: Object.fromEntries(["integrations", "connections", "plugins", "mcp", "interactions", "work", "automations", "goals", "library", "search", "permissions", "preferences", "memory", "computer_sessions", "inbox", "operations", "skills"].map((key) => [key, true])), upload_accept: { media_types: ["image/png", "image/jpeg", "image/webp", "application/pdf", "text/csv", "text/plain", "text/markdown", "text/calendar"], extensions: [".csv", ".txt", ".md", ".ics"], max_bytes: 10 * 1024 * 1024 } });
  if (head === "assistant" && id === "summary") return json(summary());
  if (head === "search") return search(url);
  if (head === "preferences") return json(preferences);
  if (head === "mutations" && id) {
    const saved = db().receipts.get(id);
    return saved ? json(saved.body, saved.status) : error(404, "not_found", "No saved receipt.");
  }
  if (head === "plugins") {
    if (id === "catalog") return list(collections.catalog, url);
    if (id === "installations" && !sub) return list(collections.installations, url);
    if (id === "installations" && sub) {
      const item = collections.installations.find((doc) => doc.id === sub);
      if (!item) return error(404, "not_found", "Not found.");
      if (segments[3] === "review") return json({ from_version: (item.package as Doc).version, to_version: url.searchParams.get("version") ?? undefined, tools_added: ["linear.cycles.list"], tools_removed: [], schemas_changed: ["linear.issues.create"], access_added: url.searchParams.get("version") ? ["Write comments on issues"] : [], access_removed: [], dependents: { automation_ids: ["auto_watch"], goal_ids: [] } });
      return json(item);
    }
  }
  if (head === "mcp" && id === "servers") {
    if (!sub) return list(collections.mcp, url);
    const item = collections.mcp.find((doc) => doc.id === sub);
    if (!item) return error(404, "not_found", "Not found.");
    if (segments[3] === "tools") return json({ items: ["issues.search", "issues.create", "issues.update", "projects.list"].map((name) => ({ name: `linear.${name}`, title: name.replace(".", " "), exposure: ((item.exposure as Doc)?.overrides as Doc | undefined)?.[`linear.${name}`] ?? (item.exposure as Doc)?.default ?? "discoverable", definition: { id: `acme/linear/${name}`, version: "0.4.1", digest: "sha256:x" }, availability: "ready" })) });
    return json(item);
  }
  if (head === "computer" && id === "sessions") return sub ? json(collections.computer.find((doc) => doc.id === sub) ?? null, collections.computer.some((doc) => doc.id === sub) ? 200 : 404) : list(collections.computer, url);
  const route = ROUTES[head];
  if (!route) return error(404, "not_found", "Not found.");
  const items = collections[route.collection];
  if (!id) {
    const bucket = url.searchParams.get("bucket");
    const status = url.searchParams.get("status");
    const connection = url.searchParams.get("connection_id");
    return list(items, url, (doc) => (!bucket || doc.bucket === bucket) && (!status || doc.status === status) && (!connection || doc.connection_id === connection));
  }
  const item = items.find((doc) => route.id(doc) === id);
  if (!item) return error(404, "not_found", "This is no longer available.");
  if (head === "connections" && sub === "resources") return json({ items: collections.resources.filter((doc) => (doc.ref as Doc).connection_id === id) });
  if (head === "automations" && sub === "occurrences") return json({ items: collections.occurrences.filter((doc) => doc.automation_id === id) });
  if (head === "library" && sub === "versions") return json({ items: [item, ...(item.version as number > 1 ? Array.from({ length: (item.version as number) - 1 }, (_, n) => ({ ...item, id: `${item.root_id}v${(item.version as number) - 1 - n}`, version: (item.version as number) - 1 - n, created_at: hours(-60 - n * 5) })) : [])] });
  if (head === "work") {
    return json({ item, interactions: collections.interactions.filter((doc) => doc.task_id === id), operations: collections.operations.filter((doc) => doc.task_id === id),
      events: [{ kind: "started", text: "Started from your message.", created_at: item.started_at }, ...(item.progress ? [{ kind: "progress", text: item.progress, created_at: hours(-0.1) }] : [])],
      result: item.status === "completed" ? "Done. The result is saved in Library." : undefined });
  }
  return json(item);
}

function receipt(request_id: string, key: string, status: number, body: unknown) {
  db().receipts.set(request_id, { key, status, body });
  return json(body, status);
}

export async function fixtureWrite(method: string, segments: string[], body: Doc) {
  const { collections, receipts } = db();
  const request_id = body.request_id as string | undefined;
  if (!request_id) return error(400, "validation_failed", "request_id is required.");
  const key = `${method} ${segments.join("/")} ${JSON.stringify(body)}`;
  const prior = receipts.get(request_id);
  if (prior) return prior.key === key ? json(prior.body, prior.status) : error(409, "idempotency_conflict", "This request ID was already used for a different change.");
  const [head, id, sub] = segments;
  const expected = body.expected_revision as number;
  const value = (body.value ?? {}) as Doc;
  const applied = (resource: Doc, status: "applied" | "queued" = "applied", work?: Doc) => receipt(request_id, key, status === "queued" ? 202 : 200, { request_id, accepted: true, status, resource, ...(work ? { work } : {}) });

  if (head === "preferences") {
    if (expected !== db().preferences.revision) return error(409, "revision_conflict", "Preferences changed elsewhere.", { current: db().preferences });
    db().preferences = { ...value, revision: (db().preferences.revision as number) + 1 };
    return applied(db().preferences);
  }
  if (head === "interactions" && sub === "responses") {
    const item = collections.interactions.find((doc) => doc.id === id);
    if (!item) return error(404, "not_found", "This question is no longer available.");
    if (item.status !== "pending" || expected !== item.revision) return error(409, "interaction_stale", "This was already answered or changed.", { current: item });
    if ((value.kind === "approve" || value.kind === "decline") && value.action_digest !== item.action_digest) return error(409, "interaction_stale", "The action changed after this approval was shown.", { current: item });
    Object.assign(item, { status: "resolved", revision: (item.revision as number) + 1, resolved_at: now(), resolution: { kind: value.kind, text: value.text, connection_id: value.connection_id, at: now() } });
    const work = collections.work.find((doc) => doc.task_id === item.task_id);
    if (work) {
      work.interaction_ids = (work.interaction_ids as string[]).filter((other) => other !== id);
      if (!(work.interaction_ids as string[]).length && work.bucket === "needs_you") work.bucket = "working";
    }
    collections.inbox.filter((doc) => (doc.link as Doc).id === id).forEach((doc) => { doc.acknowledged_at = now(); });
    return applied(item, "queued", item.task_id ? { task_id: item.task_id } : undefined);
  }
  if (head === "plugins" && id === "installations") {
    if (!sub) {
      const pkg = value.package as Doc;
      const entry = collections.catalog.find((doc) => (doc.package as Doc).id === pkg?.id);
      if (!entry) return error(400, "validation_failed", "Unknown package.");
      const created = { id: `inst_${collections.installations.length + 1}`, revision: 1, package: entry.package, pinned: false, enabled: true, state: "ready", configuration_revision: 1, approved_access: value.approved_access, required_connection_types: (entry.requested_access as Doc).connection_types, diagnostics: [], history: [{ at: now(), event: "installed", version: entry.latest_version }], created_at: now() };
      collections.installations.push(created);
      entry.installation_id = created.id;
      return applied(created);
    }
    const item = collections.installations.find((doc) => doc.id === sub);
    if (!item) return error(404, "not_found", "Not found.");
    if (expected !== item.revision) return error(409, "revision_conflict", "This plugin changed elsewhere.", { current: item });
    const action = value.action as string;
    if (action === "uninstall") {
      collections.installations.splice(collections.installations.indexOf(item), 1);
      collections.catalog.forEach((doc) => { if (doc.installation_id === item.id) delete doc.installation_id; });
      return applied({ ...item, state: "removing", revision: (item.revision as number) + 1 }, "queued");
    }
    Object.assign(item, { revision: (item.revision as number) + 1, updated_at: now(),
      ...(action === "enable" ? { enabled: true, state: "ready" } : action === "disable" ? { enabled: false, state: "disabled" } : {}),
      ...(action === "pin" ? { pinned: true } : {}),
      ...(action === "update" || action === "rollback" ? { package: { ...(item.package as Doc), version: value.version }, available_version: action === "update" ? undefined : item.available_version, history: [...(item.history as Doc[]), { at: now(), event: action === "update" ? "updated" : "rolled back", version: value.version }] } : {}) });
    return applied(item);
  }
  if (head === "mcp" && id === "servers" && sub) {
    const item = collections.mcp.find((doc) => doc.id === sub);
    if (!item) return error(404, "not_found", "Not found.");
    const action = value.action as string;
    if (action === "set_exposure") item.exposure = { ...(item.exposure as Doc), overrides: { ...(((item.exposure as Doc)?.overrides as Doc) ?? {}), [value.tool as string]: value.exposure } };
    else if (["start", "restart", "reconnect", "connect"].includes(action)) item.observed = { ...(item.observed as Doc), status: "connected", error: undefined, last_success_at: now() };
    else if (action === "stop") item.observed = { ...(item.observed as Doc), status: "disconnected" };
    item.revision = (item.revision as number) + 1;
    return applied(item);
  }
  if (head === "computer" && id === "sessions" && sub) {
    const item = collections.computer.find((doc) => doc.id === sub);
    if (!item) return error(404, "not_found", "Not found.");
    if (expected !== item.revision) return error(409, "revision_conflict", "The computer changed hands.", { current: item });
    const action = value.action as string;
    if (action === "take_over") Object.assign(item, { controller: "user", lease: { id: `lease_${Date.now()}`, holder: "user", expires_at: hours(0.25) } });
    if (action === "renew") Object.assign(item, { lease: { ...(item.lease as Doc), expires_at: hours(0.25) } });
    if (action === "release" || action === "resume") Object.assign(item, { controller: "agent", lease: undefined, blocked_reason: undefined });
    item.revision = (item.revision as number) + 1;
    return applied(item);
  }

  const route = ROUTES[head];
  if (!route) return error(404, "not_found", "Not found.");
  const items = collections[route.collection];
  if (!id) {
    if (expected !== 0) return error(400, "validation_failed", "expected_revision must be 0 for a new item.");
    const created: Doc = { ...value, id: `${head.slice(0, 4)}_${Date.now().toString(36)}`, revision: 1, created_at: now(), updated_at: now() };
    if (head === "automations") Object.assign(created, { status: "enabled", authority_rule_ids: [], next_at: hours(20) });
    if (head === "goals") Object.assign(created, { status: "active", progress: "", next_actions: [], blockers: [], linked: { task_ids: [], automation_ids: [], result_ids: [] }, completion_criteria: (value.completion_criteria as Doc[]).map((c, n) => ({ id: `c${n + 1}`, text: c.text, met: false })) });
    items.unshift(created);
    return applied(created);
  }
  const item = items.find((doc) => route.id(doc) === id);
  if (!item) return error(404, "not_found", "This is no longer available.");
  if (expected !== (item.revision ?? item.version)) return error(409, "revision_conflict", "This changed somewhere else. Showing the latest version.", { current: item });
  const bump = () => { if (item.revision !== undefined) item.revision = (item.revision as number) + 1; item.updated_at = now(); };
  if (sub === "actions") {
    const action = value.action as string;
    if (head === "connections") {
      if (action === "disconnect") { Object.assign(item, { status: "disconnected", health: { code: "ready", recovery: "connect" } }); bump(); return applied(item, "queued"); }
      if (action === "connect" || action === "repair") { bump(); Object.assign(item, { status: "connecting" }); return receipt(request_id, key, 202, { request_id, accepted: true, status: "queued", resource: item, auth: { id: `auth_${Date.now()}`, authorize_url: `/api/fixtures/v1/connections/oauth/callback?state=${item.id}&result=connected&return_to=${encodeURIComponent((value.return_to as string) || `/apps/${item.integration_id}`)}`, expires_at: hours(0.25) } }); }
      if (action === "enable" || action === "disable") { Object.assign(item, { status: action === "enable" ? "connected" : "disabled" }); bump(); return applied(item); }
    }
    if (head === "automations") {
      if (action === "pause") item.status = "paused";
      if (action === "resume") { item.status = "enabled"; delete item.snoozed_until; }
      if (action === "cancel") { item.status = "cancelled"; delete item.next_at; }
      if (action === "snooze") { item.snoozed_until = value.until; item.next_at = value.until; }
      bump();
      if (action === "run_now") return applied(item, "queued", { task_id: `task_${Date.now().toString(36)}` });
      return applied(item);
    }
    if (head === "goals") { item.status = { pause: "paused", resume: "active", complete: "completed", abandon: "abandoned" }[action] ?? item.status; if (action === "complete") item.completed_at = now(); bump(); return applied(item); }
    if (head === "permissions" && action === "revoke") { items.splice(items.indexOf(item), 1); return applied({ ...item, revision: (item.revision as number) + 1 }); }
    if (head === "memory" && action === "forget") { items.splice(items.indexOf(item), 1); return applied({ ...item, revision: (item.revision as number) + 1 }); }
    if (head === "inbox" && action === "acknowledge") { item.acknowledged_at = now(); bump(); return applied(item); }
    if (head === "library") {
      if (action === "delete") { items.splice(items.indexOf(item), 1); return applied({ ...item, deleted_at: now() }); }
      if (action === "revise") { bump(); return applied(item, "queued", { task_id: `task_${Date.now().toString(36)}` }); }
      if (action === "export") return applied(item);
    }
    if (head === "work" && action === "stop") { item.cancel_requested = true; return applied(item, "queued", { task_id: id }); }
    return error(400, "validation_failed", `Unknown action ${action}.`);
  }
  Object.assign(item, value);
  bump();
  return applied(item);
}

export function fixtureCallback(url: URL) {
  const { collections } = db();
  const item = collections.connections.find((doc) => doc.id === url.searchParams.get("state"));
  if (item) Object.assign(item, { status: "connected", revision: (item.revision as number) + 1, health: { code: "ready", recovery: "none", last_success_at: now() }, missing_scopes: [] });
  const back = url.searchParams.get("return_to") ?? "/apps";
  const target = back.startsWith("/") && !back.startsWith("//") ? back : "/apps";
  return new Response(null, { status: 303, headers: { Location: `${target}${target.includes("?") ? "&" : "?"}connection=${item?.id ?? ""}&result=${item ? "connected" : "failed"}` } });
}

export function fixturePreview(body: Doc) {
  const value = (body.value ?? body) as Doc;
  const trigger = value.trigger as Doc | undefined;
  const runs: string[] = [];
  if (trigger?.kind === "once") runs.push(trigger.at as string);
  else if (trigger?.kind === "recurring") {
    const days = /BYDAY=([A-Z,]+)/.exec(trigger.rrule as string)?.[1]?.split(",") ?? ["MO", "TU", "WE", "TH", "FR", "SA", "SU"];
    const map = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"];
    const [h, m] = ((trigger.local_start as string).split("T")[1] ?? "08:00").split(":").map(Number);
    for (let d = 0; runs.length < 5 && d < 30; d++) {
      const date = new Date(); date.setDate(date.getDate() + d); date.setHours(h, m, 0, 0);
      if (date.getTime() > Date.now() && days.includes(map[date.getDay()])) runs.push(date.toISOString());
    }
  }
  const authority = value.requested_authority as Doc | undefined;
  const classes = (authority?.action_classes as string[]) ?? ["read"];
  return json({
    next_runs: runs, timezone: trigger?.timezone ?? "Europe/London",
    proposed_rules: classes.map((cls) => ({ effect: "allow", tool_ids: [], connection_ids: value.connection_ids ?? [], resource_ids: [], action_classes: [cls] })),
    authority_summary: classes.map((cls) => ({ read: "read from the selected accounts", reversible_write: "make changes it can undo", sensitive_send: "send messages and invitations", unknown: "use tools of unknown effect", control: "" }[cls] ?? cls)).join(", ").replace(/^./, (c) => c.toUpperCase()) + ".",
    missed_run_explanation: { latest: "If Nori is offline at the scheduled time, it runs once when it is back.", skip: "If Nori is offline at the scheduled time, that run is skipped and shown in history.", all: "If Nori is offline, every missed run happens when it is back." }[value.missed_run as string] ?? "",
    warnings: classes.includes("sensitive_send") ? [{ code: "unattended_send_requires_rule", message: "This routine can send without asking you first." }] : [],
  });
}
