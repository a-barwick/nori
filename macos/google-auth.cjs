"use strict";

const { createHash, randomBytes } = require("node:crypto");
const { createServer } = require("node:http");

const authEndpoint = "https://accounts.google.com/o/oauth2/v2/auth";
const tokenEndpoint = "https://oauth2.googleapis.com/token";
const requestTimeout = 15_000;
const signInTimeout = 5 * 60_000;

class SignInError extends Error {
  constructor(message, sessionExpired = false) {
    super(message);
    this.sessionExpired = sessionExpired;
  }
}

async function receiveCode(config, openBrowser, signal) {
  const verifier = randomBytes(32).toString("base64url");
  const state = randomBytes(32).toString("base64url");
  const challenge = createHash("sha256").update(verifier).digest("base64url");
  const path = "/oauth/callback";
  let finish;
  let fail;
  const code = new Promise((resolve, reject) => { finish = resolve; fail = reject; });
  // The browser can respond before openExternal finishes.
  code.catch(() => {});
  let consumed = false;
  const server = createServer((request, response) => {
    const address = server.address();
    const host = address && `127.0.0.1:${address.port}`;
    let callback;
    try {
      callback = new URL(request.url, `http://${host}`);
    } catch {
      response.writeHead(400).end();
      return;
    }
    const valid = request.method === "GET" && request.headers.host === host &&
      callback.host === host && callback.pathname === path &&
      callback.searchParams.getAll("state").length === 1 && callback.searchParams.get("state") === state;
    if (!valid) {
      response.writeHead(400).end();
      return;
    }

    if (consumed) {
      response.writeHead(410).end();
      return;
    }
    consumed = true;
    const value = callback.searchParams.get("code");
    const succeeded = !callback.searchParams.has("error") && value && callback.searchParams.getAll("code").length === 1;
    response.writeHead(200, {
      "Content-Type": "text/plain; charset=utf-8",
      "Cache-Control": "no-store",
      "Content-Security-Policy": "default-src 'none'",
      "Referrer-Policy": "no-referrer",
      Connection: "close",
    }).end(succeeded ? "Return to Nori to finish signing in." : "Return to Nori to try signing in again.");
    if (succeeded) finish(value);
    else fail(new SignInError("Google sign-in was cancelled."));
  });

  const cancel = () => fail(new SignInError("Google sign-in was cancelled or timed out."));
  const deadline = setTimeout(cancel, signInTimeout);
  signal.addEventListener("abort", cancel, { once: true });
  try {
    await new Promise((resolve, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", resolve);
    });
    if (signal.aborted) throw new SignInError("Google sign-in was cancelled.");
    const redirectURI = `http://127.0.0.1:${server.address().port}${path}`;
    const authorization = new URL(authEndpoint);
    authorization.search = new URLSearchParams({
      client_id: config.googleClientId,
      redirect_uri: redirectURI,
      response_type: "code",
      scope: "openid email",
      code_challenge: challenge,
      code_challenge_method: "S256",
      state,
      access_type: "offline",
      prompt: "select_account consent",
    }).toString();
    await openBrowser(authorization.href);
    return { code: await code, verifier, redirectURI };
  } finally {
    clearTimeout(deadline);
    signal.removeEventListener("abort", cancel);
    server.close();
    server.closeAllConnections();
  }
}

async function exchange(config, parameters, signal) {
  const fields = new URLSearchParams({ client_id: config.googleClientId, ...parameters });
  if (config.googleClientSecret) fields.set("client_secret", config.googleClientSecret);
  let response;
  try {
    response = await fetch(tokenEndpoint, {
      method: "POST", body: fields, redirect: "error",
      signal: AbortSignal.any([signal, AbortSignal.timeout(requestTimeout)]),
    });
  } catch {
    throw new SignInError("Nori couldn’t reach Google. Try again.");
  }
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    const expired = parameters.grant_type === "refresh_token" && data.error === "invalid_grant";
    throw new SignInError(expired ? "Your Google session expired. Sign in again." : "Google couldn’t complete sign-in.", expired);
  }
  const refreshToken = data.refresh_token || parameters.refresh_token;
  if (typeof data.id_token !== "string" || !data.id_token ||
      typeof refreshToken !== "string" || !refreshToken ||
      typeof data.expires_in !== "number" || !Number.isFinite(data.expires_in) || data.expires_in <= 0) {
    throw new SignInError("Google returned an incomplete sign-in. Try again.");
  }
  return { idToken: data.id_token, refreshToken, expiresAt: Date.now() + data.expires_in * 1_000 };
}

function verifiedAccountID(token) {
  try {
    const payload = token.split(".")[1];
    const { sub } = JSON.parse(Buffer.from(payload, "base64url").toString("utf8"));
    if (typeof sub === "string" && sub.length > 0 && sub.length <= 255) return sub;
  } catch {
    // Identity is validated by IAP before being used to scope local data.
  }
  throw new SignInError("Google returned an invalid account identity.", true);
}

async function validateNoriAccess(config, token, signal) {
  let response;
  try {
    response = await fetch(new URL("/api/v1/preferences", config.webOrigin), {
      headers: { Authorization: `Bearer ${token}`, "Sec-Fetch-Site": "same-origin" },
      redirect: "manual",
      signal: AbortSignal.any([signal, AbortSignal.timeout(requestTimeout)]),
    });
  } catch {
    throw new SignInError("Nori is unreachable. Check your connection and try again.");
  }
  await response.body?.cancel().catch(() => {});
  if (response.ok) return;
  if ([302, 303, 401, 403].includes(response.status)) {
    throw new SignInError("This Google account or Mac app doesn’t have access to Nori.", true);
  }
  throw new SignInError("Nori is temporarily unavailable. Try again.");
}

class GoogleSession {
  #config;
  #keychain;
  #current;
  #refreshing;
  #controller = new AbortController();
  #writes = Promise.resolve();

  constructor(config, keychain) {
    this.#config = config;
    this.#keychain = keychain;
  }

  async restore() {
    const raw = await this.#keychain("read");
    if (!raw) return false;
    const saved = JSON.parse(raw);
    if (saved.webOrigin !== this.#config.webOrigin || saved.clientId !== this.#config.googleClientId ||
        typeof saved.idToken !== "string" || typeof saved.refreshToken !== "string" ||
        !saved.idToken || !saved.refreshToken || !Number.isFinite(saved.expiresAt)) {
      return false;
    }
    verifiedAccountID(saved.idToken);
    this.#current = saved;
    await validateNoriAccess(this.#config, await this.token(), this.#controller.signal);
    return true;
  }

  async signIn(openBrowser) {
    if (!this.#config.googleClientId) {
      throw new SignInError("Google sign-in is awaiting configuration for Nori for Mac.");
    }
    const { signal } = this.#controller;
    const callback = await receiveCode(this.#config, openBrowser, signal);
    const session = await exchange(this.#config, {
      grant_type: "authorization_code",
      code: callback.code,
      code_verifier: callback.verifier,
      redirect_uri: callback.redirectURI,
    }, signal);
    await validateNoriAccess(this.#config, session.idToken, signal);
    verifiedAccountID(session.idToken);
    await this.#save(session, signal);
  }

  async #save(session, signal) {
    const saved = { ...session, webOrigin: this.#config.webOrigin, clientId: this.#config.googleClientId };
    const write = this.#writes.then(async () => {
      signal.throwIfAborted();
      await this.#keychain("write", JSON.stringify(saved));
      signal.throwIfAborted();
      this.#current = saved;
    });
    this.#writes = write.catch(() => {});
    return write;
  }

  accountID() {
    return verifiedAccountID(this.#current.idToken);
  }

  async token() {
    if (!this.#current) throw new SignInError("Sign in to Nori again.", true);
    if (this.#current.expiresAt > Date.now() + 60_000) return this.#current.idToken;
    if (!this.#refreshing) {
      const { signal } = this.#controller;
      const accountID = this.accountID();
      this.#refreshing = exchange(this.#config, {
        grant_type: "refresh_token",
        refresh_token: this.#current.refreshToken,
      }, signal).then(async (refreshed) => {
        if (verifiedAccountID(refreshed.idToken) !== accountID) {
          throw new SignInError("Your Google account changed. Sign in again.", true);
        }
        await this.#save(refreshed, signal);
        return refreshed.idToken;
      }).finally(() => { this.#refreshing = undefined; });
    }
    return this.#refreshing;
  }

  async clear() {
    this.#controller.abort();
    this.#controller = new AbortController();
    this.#current = undefined;
    await this.#writes;
    await this.#keychain("delete");
  }
}

module.exports = { GoogleSession, SignInError, receiveCode };
