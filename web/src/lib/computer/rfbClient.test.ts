import assert from "node:assert/strict";
import { test } from "node:test";
import { NoriRFBClient, type RFBConnectionState } from "./rfbClient";

class FakeWebSocket {
  static OPEN = 1;
  static instances: FakeWebSocket[] = [];
  readyState = 1;
  binaryType = "";
  url: string;
  protocols: string[];
  sent: Uint8Array[] = [];
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((ev?: { code?: number; reason?: string }) => void) | null = null;

  constructor(url: string, protocols?: string[]) {
    this.url = url;
    this.protocols = protocols ?? [];
    FakeWebSocket.instances.push(this);
  }

  send(data: Uint8Array) {
    this.sent.push(data);
  }
  close() {
    this.readyState = 3;
  }
}

function makeFakeCanvas(): HTMLCanvasElement {
  return {
    width: 1280,
    height: 800,
    tabIndex: 0,
    style: { cursor: "" },
    getContext: () => null,
    addEventListener: () => {},
    removeEventListener: () => {},
    getBoundingClientRect: () => ({ left: 0, top: 0, width: 1280, height: 800 }),
    focus: () => {},
  } as unknown as HTMLCanvasElement;
}

function withFakeEnv(fn: (scheduled: Array<() => void>) => Promise<void>): Promise<void> {
  const origWS = globalThis.WebSocket;
  const origSetTimeout = globalThis.setTimeout;
  const origClearTimeout = globalThis.clearTimeout;
  FakeWebSocket.instances = [];
  const scheduled: Array<() => void> = [];
  (globalThis as unknown as { WebSocket: typeof FakeWebSocket }).WebSocket = FakeWebSocket;
  (globalThis as unknown as { setTimeout: (cb: () => void) => number }).setTimeout = (cb: () => void) => {
    scheduled.push(cb);
    return scheduled.length;
  };
  (globalThis as unknown as { clearTimeout: () => void }).clearTimeout = () => {};
  return fn(scheduled).finally(() => {
    globalThis.WebSocket = origWS;
    globalThis.setTimeout = origSetTimeout;
    globalThis.clearTimeout = origClearTimeout;
  });
}

function completeRFBHandshake(ws: FakeWebSocket) {
  ws.onmessage?.({ data: new TextEncoder().encode("RFB 003.008\n").buffer });
  ws.onmessage?.({ data: new Uint8Array([1, 1]).buffer }); // 1 security type: None (1)
  ws.onmessage?.({ data: new Uint8Array([0, 0, 0, 0]).buffer }); // SecurityResult OK
  const init = new Uint8Array(24);
  const v = new DataView(init.buffer);
  v.setUint16(0, 1280);
  v.setUint16(2, 800);
  v.setUint32(20, 0); // empty name
  ws.onmessage?.({ data: init.buffer });
}

test("NoriRFBClient refreshes session URL on reconnect without flashing error and reports disconnected after retries", async () => {
  await withFakeEnv(async (scheduled) => {
    const states: Array<{ state: RFBConnectionState; detail?: string }> = [];
    let refreshCount = 0;
    const client = new NoriRFBClient({
      canvas: makeFakeCanvas(),
      url: "wss://app.noribot.chat/api/v1/computer/sessions/s0/ws?token=t0",
      viewOnly: false,
      refreshURL: async () => {
        refreshCount += 1;
        return `wss://app.noribot.chat/api/v1/computer/sessions/s${refreshCount}/ws?token=t${refreshCount}`;
      },
      onStateChange: (state, detail) => states.push({ state, detail }),
    });

    client.connect();
    for (let i = 0; i < 6; i++) {
      const ws = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
      ws.onerror?.();
      ws.onclose?.();
      if (scheduled.length > 0) {
        scheduled.shift()!();
        await Promise.resolve();
        await Promise.resolve();
      }
    }

    assert.equal(refreshCount, 5);
    assert.equal(FakeWebSocket.instances.length, 6);
    assert.equal(FakeWebSocket.instances[5].url, "wss://app.noribot.chat/api/v1/computer/sessions/s5/ws?token=t5");
    assert.equal(states.some((s) => s.state === "error"), false);
    assert.deepEqual(states[states.length - 1], {
      state: "disconnected",
      detail: "Could not connect to your computer desktop.",
    });
    client.disconnect();
  });
});

test("NoriRFBClient retries when refreshURL rejects, resets attempts on connect, and aborts pending refresh on disconnect", async () => {
  await withFakeEnv(async (scheduled) => {
    const states: RFBConnectionState[] = [];
    let calls = 0;
    const pending: { resolve?: (url: string) => void } = {};
    const client = new NoriRFBClient({
      canvas: makeFakeCanvas(),
      url: "wss://app.noribot.chat/api/v1/computer/sessions/s0/ws?token=t0",
      viewOnly: true,
      refreshURL: () => {
        calls += 1;
        if (calls === 1) return Promise.reject(new Error("transient 502"));
        if (calls === 2) return Promise.resolve("wss://app.noribot.chat/api/v1/computer/sessions/s2/ws?token=t2");
        return new Promise<string>((resolve) => {
          pending.resolve = resolve;
        });
      },
      onStateChange: (state) => states.push(state),
    });

    client.connect();
    // Initial socket drops -> attempt 1 refreshURL rejects -> schedules attempt 2.
    FakeWebSocket.instances[0].onclose?.();
    scheduled.shift()!();
    await Promise.resolve();
    await Promise.resolve();
    assert.equal(calls, 1);
    assert.equal(FakeWebSocket.instances.length, 1);

    // Attempt 2 refreshURL succeeds -> opens socket 2 and completes RFB handshake (resets reconnectAttempts).
    scheduled.shift()!();
    await Promise.resolve();
    await Promise.resolve();
    assert.equal(FakeWebSocket.instances.length, 2);
    completeRFBHandshake(FakeWebSocket.instances[1]);
    assert.equal(states[states.length - 1], "connected");

    // Socket 2 drops -> starts attempt 3 refreshURL, then client disconnects while refresh is pending.
    FakeWebSocket.instances[1].onclose?.();
    scheduled.shift()!();
    await Promise.resolve();
    assert.equal(calls, 3);
    client.disconnect();
    pending.resolve?.("wss://app.noribot.chat/api/v1/computer/sessions/s3/ws?token=t3");
    await Promise.resolve();
    await Promise.resolve();
    assert.equal(FakeWebSocket.instances.length, 2, "must not open a socket after disconnect()");
  });
});

test("NoriRFBClient does not auto-reconnect or call refreshURL on server-initiated 4001-4004 close codes", async () => {
  for (const code of [4001, 4002, 4003, 4004]) {
    await withFakeEnv(async (scheduled) => {
      const states: Array<{ state: RFBConnectionState; detail?: string }> = [];
      let refreshCount = 0;
      const client = new NoriRFBClient({
        canvas: makeFakeCanvas(),
        url: "wss://app.noribot.chat/api/v1/computer/sessions/s0/ws?token=t0",
        viewOnly: false,
        refreshURL: async () => {
          refreshCount += 1;
          return `wss://app.noribot.chat/api/v1/computer/sessions/s${refreshCount}/ws?token=t${refreshCount}`;
        },
        onStateChange: (state, detail) => states.push({ state, detail }),
      });

      client.connect();
      assert.equal(FakeWebSocket.instances.length, 1);
      completeRFBHandshake(FakeWebSocket.instances[0]);
      assert.equal(states[states.length - 1]?.state, "connected");

      FakeWebSocket.instances[0].onclose?.({ code });
      await Promise.resolve();

      assert.equal(scheduled.length, 0, `close code ${code} must not schedule reconnect`);
      assert.equal(refreshCount, 0, `close code ${code} must not call refreshURL`);
      assert.equal(FakeWebSocket.instances.length, 1, `close code ${code} must not open another WebSocket`);
      assert.equal(states.some((s) => s.state === "reconnecting"), false, `close code ${code} must not enter reconnecting`);
      const last = states[states.length - 1];
      assert.equal(last?.state, "disconnected", `close code ${code} should transition to disconnected`);
      assert.ok(last?.detail && last.detail.length > 0, `close code ${code} should include calm user-facing copy`);
      client.disconnect();
    });
  }
});
