import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { privacyOptions, safeSpanName } from "./privacy";

describe("observability privacy safeSpanName", () => {
  test("allows top-level /memory spans and strips query/hash fragments", () => {
    assert.equal(safeSpanName("/memory"), "/memory");
    assert.equal(safeSpanName("GET /memory"), "GET /memory");
    assert.equal(safeSpanName("/memory?tab=logs&q=private-secret"), "/memory");
    assert.equal(safeSpanName("/memory#core"), "/memory");
  });

  test("redacts nested /memory subpaths with dates or IDs to operation", () => {
    assert.equal(safeSpanName("/memory/logs/2026-11-23"), "operation");
    assert.equal(safeSpanName("GET /memory/logs/2026-11-23"), "operation");
    assert.equal(safeSpanName("/memory/working/si_12345"), "operation");
    assert.equal(safeSpanName("/api/v1/memory/logs/2026-11-23"), "operation");
  });

  test("preserves other allowlisted routes and nori.submit_to_reply", () => {
    assert.equal(safeSpanName("/"), "/");
    assert.equal(safeSpanName("/more"), "/more");
    assert.equal(safeSpanName("/library"), "/library");
    assert.equal(safeSpanName("/settings"), "/settings");
    assert.equal(safeSpanName("POST /api/v1"), "POST /api/v1");
    assert.equal(safeSpanName("nori.submit_to_reply"), "nori.submit_to_reply");
    assert.equal(safeSpanName("/unknown/secret-path"), "operation");
  });

  test("beforeSendSpan applies safeSpanName to span name and route attributes", () => {
    const { beforeSendSpan } = privacyOptions("0.1");
    const baseSpan = {
      trace_id: "0123456789abcdef0123456789abcdef",
      span_id: "0123456789abcdef",
      start_timestamp: 1732400000,
      status: "ok" as const,
      is_segment: true,
    };
    const allowed = beforeSendSpan({
      ...baseSpan,
      name: "GET /memory?tab=core&id=mem_1",
      attributes: {
        "http.route": "/memory",
        "sentry.transaction": "/memory?q=secret",
        "nori.mode": "local",
      },
    });
    assert.equal(allowed.name, "GET /memory");
    assert.equal(allowed.attributes["http.route"], "/memory");
    assert.equal(allowed.attributes["sentry.transaction"], "/memory");
    assert.equal(allowed.attributes["nori.mode"], "local");

    const redacted = beforeSendSpan({
      ...baseSpan,
      name: "GET /memory/logs/2026-11-23",
      attributes: {
        "http.route": "/memory/logs/2026-11-23",
      },
    });
    assert.equal(redacted.name, "operation");
    assert.equal(redacted.attributes["http.route"], "operation");
  });

  test("preserves correlation IDs and opaque user.id while stripping PII", () => {
    const { beforeSend, beforeSendSpan } = privacyOptions("0.1");
    const userID = "22222222-2222-4222-8222-222222222222";
    const span = beforeSendSpan({
      trace_id: "0123456789abcdef0123456789abcdef",
      span_id: "0123456789abcdef",
      start_timestamp: 1732400000,
      status: "ok" as const,
      is_segment: true,
      name: "nori.submit_to_reply",
      attributes: {
        "nori.user.id": userID,
        "nori.task.id": "tsk_100",
        "nori.turn.id": "turn_7",
        "nori.client.platform": "ios",
        "secret.attr": "drop-me",
      },
    });
    assert.equal(span.attributes["nori.user.id"], userID);
    assert.equal(span.attributes["nori.task.id"], "tsk_100");
    assert.equal(span.attributes["nori.turn.id"], "turn_7");
    assert.equal(span.attributes["nori.client.platform"], "ios");
    assert.equal(span.attributes["secret.attr"], undefined);

    const event = beforeSend(
      {
        event_id: "0123456789abcdef0123456789abcdef",
        type: undefined,
        user: { id: userID, email: "user@example.com", username: "user" },
        tags: {
          "nori.user.id": userID,
          "nori.task.id": "tsk_100",
          "nori.client.platform": "web",
        },
      } as Parameters<typeof beforeSend>[0],
      {}
    );
    assert.deepEqual(event?.user, { id: userID });
    assert.equal(event?.tags?.["nori.user.id"], userID);
    assert.equal(event?.tags?.["nori.task.id"], "tsk_100");
    assert.equal(event?.tags?.["nori.client.platform"], "web");
  });
});
