"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { GoogleSession, SignInError } = require("../google-auth.cjs");

function fakeJWT(sub) {
  const header = Buffer.from(JSON.stringify({ alg: "RS256" })).toString("base64url");
  const payload = Buffer.from(JSON.stringify({ sub })).toString("base64url");
  return `${header}.${payload}.sig`;
}

function makeKeychain(initial) {
  let stored = initial;
  const ops = [];
  return {
    ops,
    async fn(action, value) {
      ops.push(action);
      if (action === "read") return stored;
      if (action === "write") {
        stored = value;
        return undefined;
      }
      if (action === "delete") {
        stored = undefined;
        return undefined;
      }
      throw new Error(`Unexpected keychain action: ${action}`);
    },
  };
}

test("GoogleSession.token coalesces concurrent refreshes into a single token exchange", async () => {
  const config = {
    webOrigin: "https://nori.example",
    googleClientId: "desktop.apps.googleusercontent.com",
    googleClientSecret: "secret",
  };
  const expiredSession = {
    webOrigin: config.webOrigin,
    clientId: config.googleClientId,
    idToken: fakeJWT("user-123"),
    refreshToken: "refresh-1",
    expiresAt: Date.now() - 10_000,
  };
  const keychain = makeKeychain(JSON.stringify(expiredSession));
  const session = new GoogleSession(config, keychain.fn);

  const originalFetch = globalThis.fetch;
  let tokenRequests = 0;
  let releaseTokenResponse;
  const tokenResponseReady = new Promise((resolve) => { releaseTokenResponse = resolve; });

  globalThis.fetch = async (url) => {
    const href = String(url);
    if (href === "https://oauth2.googleapis.com/token") {
      tokenRequests += 1;
      await tokenResponseReady;
      return new Response(JSON.stringify({
        id_token: fakeJWT("user-123"),
        refresh_token: "refresh-2",
        expires_in: 3600,
      }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    if (href === "https://nori.example/api/v1/preferences") {
      return new Response("{}", { status: 200 });
    }
    throw new Error(`Unexpected fetch URL: ${href}`);
  };

  try {
    // Trigger restore (which refreshes once), then expire and fire 5 concurrent token() calls.
    releaseTokenResponse();
    assert.equal(await session.restore(), true);
    assert.equal(tokenRequests, 1);

    // Re-seed an expired session to test concurrent token() coalescing directly.
    const session2 = new GoogleSession(config, makeKeychain(JSON.stringify(expiredSession)).fn);
    tokenRequests = 0;
    let releaseConcurrent;
    const gate = new Promise((resolve) => { releaseConcurrent = resolve; });

    globalThis.fetch = async (url) => {
      const href = String(url);
      if (href === "https://oauth2.googleapis.com/token") {
        tokenRequests += 1;
        await gate;
        return new Response(JSON.stringify({
          id_token: fakeJWT("user-123"),
          refresh_token: "refresh-3",
          expires_in: 3600,
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      return new Response("{}", { status: 200 });
    };

    // Prime #current without refreshing by seeding an initially fresh token, then calling restore,
    // OR by running two concurrent restore() / token() calls on an expired keychain record.
    // Let's seed a token expiring inside the 60s refresh window so restore() triggers a refresh,
    // and call token() concurrently while restore's refresh is in flight.
    const restorePromise = session2.restore();
    // Wait a tick for restore() to read keychain and enter token().
    await new Promise((resolve) => setImmediate(resolve));
    const t1 = session2.token();
    const t2 = session2.token();
    const t3 = session2.token();
    releaseConcurrent();

    const [restored, v1, v2, v3] = await Promise.all([restorePromise, t1, t2, t3]);
    assert.equal(restored, true);
    assert.equal(tokenRequests, 1);
    assert.equal(v1, v2);
    assert.equal(v2, v3);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("GoogleSession.token marks invalid_grant refresh failures as sessionExpired", async () => {
  const config = {
    webOrigin: "https://nori.example",
    googleClientId: "desktop.apps.googleusercontent.com",
  };
  const expiredSession = {
    webOrigin: config.webOrigin,
    clientId: config.googleClientId,
    idToken: fakeJWT("user-123"),
    refreshToken: "revoked-refresh",
    expiresAt: Date.now() - 10_000,
  };
  const session = new GoogleSession(config, makeKeychain(JSON.stringify(expiredSession)).fn);

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({ error: "invalid_grant" }), {
    status: 400,
    headers: { "Content-Type": "application/json" },
  });
  try {
    await assert.rejects(() => session.restore(), (error) => {
      assert.equal(error instanceof SignInError, true);
      assert.equal(error.sessionExpired, true);
      return true;
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
});
