import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { Thread } from "@/components/chat/Thread";
import { AssistantProvider } from "@/lib/assistant/context";
import {
  buildApprovalResponse,
  formatInteractionExpiry,
  invalidationPrefixesForResponse,
  mergeInlineInteractions,
  resolvedStatusPill,
  resolvedSummaryText,
  resolveInteractionLocally,
  responseAnnouncement,
  submitInteractionResponse,
} from "@/lib/assistant/interactions";
import {
  buildPermissionRuleEditable,
  countRulesByEffect,
  describePermissionRulePreview,
  groupPermissionRules,
  revokeRuleConfirmationBody,
  RULE_EFFECT_META,
  RULE_PRECEDENCE_NOTE,
} from "@/lib/assistant/permissions";
import { useResource } from "@/lib/assistant/useResource";
import { asFetch } from "@/lib/test-fetch";
import type { Connection, Interaction, PermissionRule } from "@/lib/contracts/assistant";
import { InteractionCard } from "./InteractionCard";
import { NewRule } from "./SettingsScreen";

function makeApprovalInteraction(overrides: Partial<Extract<Interaction, { kind: "approval" }>> = {}): Extract<Interaction, { kind: "approval" }> {
  return {
    id: "int_approval_1",
    revision: 1,
    task_id: "task_send_1",
    task_title: "Send project update",
    instruction_revision: 2,
    conversation_id: "assistant",
    kind: "approval",
    status: "pending",
    prompt: 'Send "Q4 update" to external@example.com',
    action_digest: "sha256:act_digest_123",
    expires_at: new Date(Date.now() + 30 * 60_000).toISOString(),
    created_at: "2026-11-24T19:30:00Z",
    payload: {
      summary: 'Send "Q4 update" from you@example.com to external@example.com:\nHere is the latest status.',
      tool_id: "nori.google/mail/messages.send",
      warning: "This email goes to an external recipient and cannot be unsent.",
      effect: "sensitive_send",
      account: { connection_id: "conn_mail_1", label: "you@example.com" },
      resources: [],
      recipients: ["external@example.com"],
      material_effects: ["Sends an email immediately"],
    },
    ...overrides,
  };
}

function makeRule(overrides: Partial<PermissionRule>): PermissionRule {
  return {
    id: "rule_1",
    revision: 1,
    effect: "allow",
    tool_ids: ["*"],
    connection_ids: ["conn_1"],
    resource_ids: ["*"],
    action_classes: ["reversible_write"],
    title: "Calendar edits",
    ...overrides,
  };
}

describe("3-button Grok-Bot approval payloads and cache invalidation", () => {
  test("buildApprovalResponse emits exact wire shapes for Allow once, Always allow, and Deny", () => {
    const once = buildApprovalResponse("allow_once", "sha256:abc");
    assert.deepEqual(once, { kind: "approve", action_digest: "sha256:abc" });
    assert.equal("remember_rule" in once, false);

    const always = buildApprovalResponse("always_allow", "sha256:abc");
    assert.deepEqual(always, { kind: "approve", action_digest: "sha256:abc", remember_rule: true });

    const deny = buildApprovalResponse("deny", "sha256:abc");
    assert.deepEqual(deny, { kind: "decline", action_digest: "sha256:abc" });
    assert.equal("remember_rule" in deny, false);
  });

  test("invalidationPrefixesForResponse invalidates /permissions only when remember_rule is true", () => {
    const oncePrefixes = invalidationPrefixesForResponse(buildApprovalResponse("allow_once", "sha256:abc"));
    assert.equal(oncePrefixes.includes("/interactions"), true);
    assert.equal(oncePrefixes.includes("/work"), true);
    assert.equal(oncePrefixes.includes("/assistant/summary"), true);
    assert.equal(oncePrefixes.includes("/permissions"), false);

    const alwaysPrefixes = invalidationPrefixesForResponse(buildApprovalResponse("always_allow", "sha256:abc"));
    assert.equal(alwaysPrefixes.includes("/interactions"), true);
    assert.equal(alwaysPrefixes.includes("/work"), true);
    assert.equal(alwaysPrefixes.includes("/assistant/summary"), true);
    assert.equal(alwaysPrefixes.includes("/permissions"), true);

    const denyPrefixes = invalidationPrefixesForResponse(buildApprovalResponse("deny", "sha256:abc"));
    assert.equal(denyPrefixes.includes("/interactions"), true);
    assert.equal(denyPrefixes.includes("/permissions"), false);

    assert.equal(responseAnnouncement(buildApprovalResponse("allow_once", "sha256:abc")), "Allowed once.");
    assert.equal(responseAnnouncement(buildApprovalResponse("always_allow", "sha256:abc")), "Approved and rule saved.");
    assert.equal(responseAnnouncement(buildApprovalResponse("deny", "sha256:abc")), "Denied.");
  });

  test("formatInteractionExpiry formats future 30m expiry and past timestamps cleanly", () => {
    const now = Date.parse("2026-11-24T20:00:00Z");
    assert.equal(formatInteractionExpiry("2026-11-24T20:30:00Z", now), "Expires in 30 minutes");
    assert.equal(formatInteractionExpiry("2026-11-24T19:55:00Z", now), "Expired");
    assert.equal(formatInteractionExpiry(undefined, now), undefined);
    assert.equal(formatInteractionExpiry("not-a-date", now), undefined);
  });
});

describe("submitInteractionResponse wire calls and live cache invalidation", () => {
  let origFetch: typeof globalThis.fetch;

  beforeEach(() => {
    origFetch = globalThis.fetch;
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
  });

  test("Always allow sends remember_rule: true and refreshes /permissions, /interactions, /work, and /assistant/summary", async () => {
    const interaction = makeApprovalInteraction({ id: `int_always_${crypto.randomUUID()}` });
    const calls: { url: string; method: string; body?: Record<string, unknown> }[] = [];


    globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
      calls.push({ url, method, body });

      if (url === `/api/v1/interactions/${encodeURIComponent(interaction.id)}` && method === "GET") {
        return Response.json(interaction);
      }
      if (url === `/api/v1/interactions/${encodeURIComponent(interaction.id)}/responses` && method === "POST") {
        return Response.json({
          request_id: body?.request_id,
          accepted: true,
          status: "applied",
          resource: {
            ...interaction,
            status: "resolved",
            revision: interaction.revision + 1,
            resolution: { kind: "approve", at: "2026-11-24T20:01:00Z" },
          },
        });
      }
      if (url === "/api/v1/permissions") {
        return Response.json({ items: [makeRule({ id: "rule_saved_1", effect: "allow" })] });
      }
      if (url.startsWith("/api/v1/interactions")) {
        return Response.json({ items: [] });
      }
      return Response.json({ items: [] });
    });

    const invalidated: string[] = [];
    const result = await submitInteractionResponse(
      interaction,
      buildApprovalResponse("always_allow", interaction.action_digest!),
      (...prefixes) => invalidated.push(...prefixes),
    );

    assert.equal(result.ok, true);
    if (result.ok) {
      assert.equal(result.rememberRule, true);
      assert.equal(result.resolved.status, "resolved");
      assert.equal(result.resolved.resolution?.kind, "approve");
    }

    const post = calls.find((call) => call.method === "POST");
    assert.ok(post);
    assert.deepEqual(post.body?.value, {
      kind: "approve",
      action_digest: interaction.action_digest,
      remember_rule: true,
    });

    assert.equal(invalidated.includes("/permissions"), true);
    assert.equal(invalidated.includes("/interactions"), true);
    assert.equal(invalidated.includes("/work"), true);
    assert.equal(invalidated.includes("/assistant/summary"), true);
  });

  test("Allow once omits remember_rule and does not refresh /permissions", async () => {
    const interaction = makeApprovalInteraction({ id: `int_once_${crypto.randomUUID()}` });
    const calls: { url: string; method: string; body?: Record<string, unknown> }[] = [];


    globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
      calls.push({ url, method, body });

      if (url === `/api/v1/interactions/${encodeURIComponent(interaction.id)}` && method === "GET") {
        return Response.json(interaction);
      }
      if (url === `/api/v1/interactions/${encodeURIComponent(interaction.id)}/responses` && method === "POST") {
        return Response.json({
          request_id: body?.request_id,
          accepted: true,
          status: "applied",
          resource: {
            ...interaction,
            status: "resolved",
            revision: interaction.revision + 1,
            resolution: { kind: "approve", at: "2026-11-24T20:01:00Z" },
          },
        });
      }
      return Response.json({ items: [] });
    });

    const invalidated: string[] = [];
    const result = await submitInteractionResponse(
      interaction,
      buildApprovalResponse("allow_once", interaction.action_digest!),
      (...prefixes) => invalidated.push(...prefixes),
    );

    assert.equal(result.ok, true);
    if (result.ok) {
      assert.equal(result.rememberRule, false);
    }

    const post = calls.find((call) => call.method === "POST");
    assert.ok(post);
    assert.deepEqual(post.body?.value, {
      kind: "approve",
      action_digest: interaction.action_digest,
    });
    assert.equal("remember_rule" in (post.body?.value as Record<string, unknown>), false);

    assert.equal(invalidated.includes("/permissions"), false);
    assert.equal(invalidated.includes("/interactions"), true);
  });

  test("Deny sends decline with action_digest", async () => {
    const interaction = makeApprovalInteraction({ id: `int_deny_${crypto.randomUUID()}` });
    const calls: { url: string; method: string; body?: Record<string, unknown> }[] = [];

    globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
      calls.push({ url, method, body });

      if (url === `/api/v1/interactions/${encodeURIComponent(interaction.id)}` && method === "GET") {
        return Response.json(interaction);
      }
      return Response.json({
        request_id: body?.request_id,
        accepted: true,
        status: "applied",
        resource: resolveInteractionLocally(interaction, buildApprovalResponse("deny", interaction.action_digest!)),
      });
    });

    const result = await submitInteractionResponse(
      interaction,
      buildApprovalResponse("deny", interaction.action_digest!),
    );
    assert.equal(result.ok, true);
    const post = calls.find((call) => call.method === "POST");
    assert.deepEqual(post?.body?.value, {
      kind: "decline",
      action_digest: interaction.action_digest,
    });
    assert.equal(typeof useResource, "function");
  });
});

describe("InteractionCard and inline chat stream rendering", () => {
  test("shows the full send details and only permits approval once when requested", () => {
    const base = makeApprovalInteraction();
    const interaction = makeApprovalInteraction({
      payload: {
        ...base.payload,
        approval_once: true,
        tool_id: "google/mail/mail-send",
        summary: "Send email\nTo: first@example.com\nCc: second@example.com\nSubject: Update\nAttachments: plan.pdf\n\n  Full message\nwith spacing.  ",
        recipients: ["first@example.com", "second@example.com"],
      },
    });
    const html = renderToStaticMarkup(
      <AssistantProvider localMode>
        <InteractionCard interaction={interaction} />
      </AssistantProvider>,
    );

    assert.match(html, /Allow once/);
    assert.doesNotMatch(html, /Always allow/);
    assert.match(html, /first@example.com/);
    assert.match(html, /second@example.com/);
    assert.equal(html.match(/first@example\.com/g)?.length, 1);
    assert.equal(html.match(/second@example\.com/g)?.length, 1);
    assert.match(html, /plan.pdf/);
    assert.ok(html.includes("  Full message\nwith spacing.  "));
  });

  test("renders 3-button approval card with warning callout and expiry indicator without raw task link", () => {
    const interaction = makeApprovalInteraction();
    const html = renderToStaticMarkup(
      <AssistantProvider localMode>
        <InteractionCard interaction={interaction} inline />
      </AssistantProvider>,
    );

    assert.match(html, /Allow once/);
    assert.match(html, /Always allow/);
    assert.match(html, /Deny/);
    assert.match(html, /data-approval-warning/);
    assert.match(html, /This email goes to an external recipient and cannot be unsent\./);
    assert.match(html, /Expires in 30 minutes/);
    assert.equal(html.includes("/work/task_send_1"), false);
  });

  test("omits warning callout when payload.warning is absent", () => {
    const base = makeApprovalInteraction();
    const interaction = makeApprovalInteraction({
      payload: { ...base.payload, warning: undefined },
    });
    const html = renderToStaticMarkup(
      <AssistantProvider localMode>
        <InteractionCard interaction={interaction} />
      </AssistantProvider>,
    );

    assert.equal(html.includes("data-approval-warning"), false);
    assert.match(html, /Allow once/);
    assert.match(html, /Always allow/);
    assert.match(html, /Deny/);
  });

  test("renders resolved approval confirmation states cleanly", () => {
    const resolvedAlways = resolveInteractionLocally(
      makeApprovalInteraction(),
      buildApprovalResponse("always_allow", "sha256:act_digest_123"),
      undefined,
      "2026-11-24T20:01:00Z",
    );
    assert.deepEqual(resolvedStatusPill(resolvedAlways), { tone: "ok", label: "Approved" });
    assert.equal(resolvedSummaryText(resolvedAlways, true), "Always allowed · Rule saved");
    assert.equal(resolvedSummaryText(resolvedAlways, false), "Allowed once");

    const htmlAlways = renderToStaticMarkup(
      <AssistantProvider localMode>
        <InteractionCard interaction={resolvedAlways} inline rememberedRule />
      </AssistantProvider>,
    );
    assert.match(htmlAlways, /Approved/);
    assert.match(htmlAlways, /Always allowed · Rule saved/);
    assert.equal(htmlAlways.includes("Allow once"), false);

    const resolvedDenied = resolveInteractionLocally(
      makeApprovalInteraction(),
      buildApprovalResponse("deny", "sha256:act_digest_123"),
      undefined,
      "2026-11-24T20:01:00Z",
    );
    assert.deepEqual(resolvedStatusPill(resolvedDenied), { tone: "neutral", label: "Denied" });
    assert.equal(resolvedSummaryText(resolvedDenied), "Denied");
  });

  test("mergeInlineInteractions keeps session-resolved cards visible alongside pending cards in stable order", () => {
    const first = makeApprovalInteraction({ id: "int_1", created_at: "2026-11-24T19:00:00Z" });
    const second = makeApprovalInteraction({ id: "int_2", created_at: "2026-11-24T19:05:00Z" });
    const resolvedFirst = resolveInteractionLocally(
      first,
      buildApprovalResponse("always_allow", first.action_digest!),
      undefined,
      "2026-11-24T19:06:00Z",
    );

    const mergedBeforeRefetch = mergeInlineInteractions([first, second], {
      [first.id]: { interaction: resolvedFirst, rememberedRule: true },
    });
    assert.deepEqual(
      mergedBeforeRefetch.map((entry) => [entry.interaction.id, entry.interaction.status, entry.rememberedRule]),
      [
        ["int_1", "resolved", true],
        ["int_2", "pending", undefined],
      ],
    );

    const mergedAfterRefetch = mergeInlineInteractions([second], {
      [first.id]: { interaction: resolvedFirst, rememberedRule: true },
    });
    assert.deepEqual(
      mergedAfterRefetch.map((entry) => [entry.interaction.id, entry.interaction.status]),
      [
        ["int_1", "resolved"],
        ["int_2", "pending"],
      ],
    );
  });

  test("Thread renders pending and session-resolved interactions inline at the bottom of the chat stream", () => {
    const pendingCard = makeApprovalInteraction({ id: "int_live_1", created_at: "2026-11-24T19:10:00Z" });
    const resolvedCard = resolveInteractionLocally(
      makeApprovalInteraction({ id: "int_done_1", created_at: "2026-11-24T19:05:00Z" }),
      buildApprovalResponse("always_allow", "sha256:act_digest_123"),
      undefined,
      "2026-11-24T19:06:00Z",
    );
    const merged = mergeInlineInteractions([pendingCard], {
      [resolvedCard.id]: { interaction: resolvedCard, rememberedRule: true },
    });

    const html = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [{ id: "m1", sender: "me", at: Date.parse("2026-11-24T19:00:00Z"), text: "Send the update" }],
          }}
          interactions={merged}
          status="1 task in progress · 1 waiting for you"
          emptyMessage="Say hello"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );

    assert.match(html, /data-inline-interactions/);
    assert.match(html, /Always allowed · Rule saved/);
    assert.match(html, /Allow once/);
    assert.match(html, /Always allow/);
    assert.match(html, /Deny/);
    const messageIdx = html.indexOf("Send the update");
    const inlineIdx = html.indexOf("data-inline-interactions");
    const statusIdx = html.indexOf("1 task in progress");
    assert.ok(messageIdx >= 0 && inlineIdx > messageIdx && statusIdx > inlineIdx);

    // A newer user message sent AFTER the cards must appear BELOW them chronologically.
    const chronoHtml = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [
              { id: "m1", sender: "me", at: Date.parse("2026-11-24T19:00:00Z"), text: "Send the update" },
              { id: "m2", sender: "me", at: Date.parse("2026-11-24T19:15:00Z"), text: "Nevermind, next topic" },
            ],
          }}
          interactions={merged}
          emptyMessage="Say hello"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );
    const firstMsgIdx = chronoHtml.indexOf("Send the update");
    const cardIdx = chronoHtml.indexOf("int_live_1");
    const laterMsgIdx = chronoHtml.indexOf("Nevermind, next topic");
    assert.ok(firstMsgIdx >= 0 && cardIdx > firstMsgIdx && laterMsgIdx > cardIdx);
  });
});

describe("3-state custom permission rules UI and helpers", () => {
  test("RULE_EFFECT_META and RULE_PRECEDENCE_NOTE cover allow, ask, and deny with exact labels", () => {
    assert.equal(RULE_EFFECT_META.allow.label, "Allow automatically");
    assert.equal(RULE_EFFECT_META.ask.label, "Ask first");
    assert.equal(RULE_EFFECT_META.deny.label, "Never allow");
    assert.equal(
      RULE_PRECEDENCE_NOTE,
      "Never allow beats everything; Ask first always overrides Allow automatically.",
    );
  });

  test("groupPermissionRules and countRulesByEffect organize deny > ask > allow and filter cleanly", () => {
    const rules: PermissionRule[] = [
      makeRule({ id: "r_allow", effect: "allow" }),
      makeRule({ id: "r_ask", effect: "ask", action_classes: ["sensitive_send"] }),
      makeRule({ id: "r_deny", effect: "deny", action_classes: ["sensitive_send"] }),
      makeRule({ id: "r_auto", effect: "allow", automation_id: "auto_1" }),
    ];

    assert.deepEqual(countRulesByEffect(rules), { allow: 2, ask: 1, deny: 1 });

    const allGroups = groupPermissionRules(rules, "all");
    assert.deepEqual(
      allGroups.map((group) => [group.id, group.title, group.rules.map((r) => r.id)]),
      [
        ["deny", "Never allow", ["r_deny"]],
        ["ask", "Ask first", ["r_ask"]],
        ["allow", "Allow automatically", ["r_allow"]],
        ["scoped", "For automations", ["r_auto"]],
      ],
    );

    const askOnly = groupPermissionRules(rules, "ask").filter((g) => g.rules.length > 0);
    assert.equal(askOnly.length, 1);
    assert.equal(askOnly[0].id, "ask");
    assert.deepEqual(askOnly[0].rules.map((r) => r.id), ["r_ask"]);
  });

  test("describePermissionRulePreview, buildPermissionRuleEditable, and revokeRuleConfirmationBody handle all 3 effects", () => {
    assert.equal(
      describePermissionRulePreview({ effect: "allow", classes: ["reversible_write"], targetLabel: "Work" }),
      "Nori will automatically make undoable changes with Work.",
    );
    assert.equal(
      describePermissionRulePreview({
        effect: "ask",
        classes: ["sensitive_send"],
        targetLabel: "Personal",
        recipients: "boss@example.com",
      }),
      "Nori will always ask first before it can send with Personal to boss@example.com.",
    );
    assert.equal(
      describePermissionRulePreview({ effect: "deny", classes: ["sensitive_send"], targetLabel: "all tools & MCP servers" }),
      "Nori will never send with all tools & MCP servers.",
    );

    const askPayload = buildPermissionRuleEditable({
      effect: "ask",
      account: "conn_1",
      classes: ["sensitive_send"],
      recipients: "",
    });
    assert.equal(askPayload.effect, "ask");
    assert.equal("recipients" in askPayload, false);

    const allowPayload = buildPermissionRuleEditable({
      effect: "allow",
      account: "conn_1",
      classes: ["sensitive_send"],
      recipients: "a@example.com, b@example.com",
    });
    assert.deepEqual(allowPayload.recipients, ["a@example.com", "b@example.com"]);

    assert.match(revokeRuleConfirmationBody(makeRule({ effect: "deny" })), /block/);
    assert.match(revokeRuleConfirmationBody(makeRule({ effect: "ask" })), /other rules and defaults/);
    assert.match(revokeRuleConfirmationBody(makeRule({ effect: "allow" })), /ask you before/);
  });

  test("NewRule builder renders all 3 rule effect options", () => {
    const connections: Connection[] = [
      {
        id: "conn_1",
        revision: 1,
        integration_id: "google",
        title: "Google",
        account: { provider_id: "p1", label: "you@example.com" },
        status: "connected",
        granted_scopes: [],
        selected_resource_ids: [],
        health: {},
        defaults: [],
      },
    ];
    const html = renderToStaticMarkup(<NewRule connections={connections} servers={[]} onDone={() => {}} />);

    assert.match(html, /<option value="allow"[^>]*>Allow automatically<\/option>/);
    assert.match(html, /<option value="ask"[^>]*>Ask first<\/option>/);
    assert.match(html, /<option value="deny"[^>]*>Never allow<\/option>/);
  });
});
