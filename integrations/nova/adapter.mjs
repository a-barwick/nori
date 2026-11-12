const conversation = "/v1/conversations/assistant";
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const resource = "[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}";
const readPath = new RegExp(`^/(?:work|interactions|mutations|results)/${resource}$`);
const responsePath = new RegExp(`^/interactions/${resource}/responses$`);
const maxBody = 64 * 1024;

const privateHeaders = { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" };
const json = (value, status) => Response.json(value, { status, headers: privateHeaders });
const error = (status, message) => json({ error: message }, status);
const principalKey = ({ issuer, subject }) => JSON.stringify([issuer, subject]);
const validPrincipal = (identity) => typeof identity?.issuer === "string" && identity.issuer &&
  typeof identity?.subject === "string" && identity.subject;

function routeFor(request) {
  const url = new URL(request.url);
  if (!url.pathname.startsWith("/api/nori/")) return null;
  const path = url.pathname.slice("/api/nori".length);

  if (request.method === "GET") {
    if (path === "/conversation") return { path: conversation };
    if (path === "/events") {
      const after = request.headers.get("Last-Event-ID") || url.searchParams.get("after");
      if (after && !/^[0-9]{1,19}$/.test(after)) return { invalidCursor: true };
      const query = after ? `?after=${after}` : "";
      return { path: `${conversation}/events${query}`, stream: true };
    }
    if (path === "/work" || readPath.test(path)) return { path: `/v1${path}` };
    if (path === "/interactions") {
      const query = new URLSearchParams({ status: "pending" });
      const cursor = url.searchParams.get("cursor");
      if (cursor !== null) query.set("cursor", cursor);
      return { path: `/v1/interactions?${query}` };
    }
  }
  if (request.method === "POST") {
    if (path === "/messages") return { path: `${conversation}/messages`, mutation: "message" };
    if (responsePath.test(path)) return { path: `/v1${path}`, mutation: "approval" };
  }
  return null;
}

async function readInput(request) {
  const reader = request.body?.getReader();
  if (!reader) return undefined;
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let size = 0;
  let text = "";
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return JSON.parse(text + decoder.decode());
      size += value.byteLength;
      if (size > maxBody) {
        await reader.cancel();
        return undefined;
      }
      text += decoder.decode(value, { stream: true });
    }
  } catch {
    await reader.cancel().catch(() => {});
    return undefined;
  } finally {
    reader.releaseLock();
  }
}

function hasKeys(value, keys) {
  return value && typeof value === "object" && !Array.isArray(value) &&
    Object.keys(value).length === keys.length && keys.every((key) => Object.hasOwn(value, key));
}

function validMutation(kind, body) {
  if (kind === "message") {
    return hasKeys(body, ["id", "text"]) && typeof body.id === "string" && uuid.test(body.id) &&
      typeof body.text === "string" && body.text.trim() && new TextEncoder().encode(body.text.trim()).length <= 8192;
  }
  return hasKeys(body, ["request_id", "expected_revision", "value"]) &&
    typeof body.request_id === "string" && uuid.test(body.request_id) &&
    Number.isSafeInteger(body.expected_revision) && body.expected_revision >= 1 &&
    hasKeys(body.value, ["kind", "action_digest"]) && ["approve", "decline"].includes(body.value.kind) &&
    typeof body.value.action_digest === "string" && body.value.action_digest.trim().length > 0;
}

function mediaType(message) {
  return message.headers.get("content-type")?.split(";", 1)[0].trim().toLowerCase();
}

async function mutationResponse(upstream, route, body) {
  if (mediaType(upstream) !== "application/json") return relay(upstream, "application/json");
  if (route.mutation === "message" && upstream.status === 202) {
    return json({ ...await upstream.json(), turnId: body.id, state: "accepted", accepted: true, retryable: false }, 202);
  }
  if (upstream.status === 503) {
    const result = await upstream.json();
    if (result?.accepted === true) {
      const id = route.mutation === "message" ? { turnId: body.id } : { request_id: body.request_id };
      return json({ ...result, ...id, state: "queued", retryable: true }, 202);
    }
    return json(result, 503);
  }
  return relay(upstream, "application/json");
}

async function relay(upstream, contentType) {
  if (mediaType(upstream) !== contentType) {
    await upstream.body?.cancel();
    return error(502, "Nori returned an unexpected response.");
  }
  const headers = { ...privateHeaders, "Content-Type": contentType };
  if (contentType === "text/event-stream") {
    headers["Cache-Control"] = "no-store, no-transform";
    headers["X-Accel-Buffering"] = "no";
  }
  return new Response(upstream.body, { status: upstream.status, headers });
}

/**
 * Mount this Fetch handler under /api/nori. verifyIdentity must validate the host
 * session and return its stable issuer and subject, or null. Bindings are trusted
 * provisioning data. serviceAuthorization returns the runtime's Authorization
 * header value, using only server-held credentials.
 * @param {{
 *   runtimeBase: string,
 *   verifyIdentity: (request: Request) => Promise<{issuer: string, subject: string} | null>,
 *   bindings: ReadonlyArray<{issuer: string, subject: string, noriUserId: string}>,
 *   serviceAuthorization: (signal: AbortSignal) => Promise<string>,
 * }} config
 * @returns {(request: Request) => Promise<Response>}
 */
export function createNoriHandler({ runtimeBase, verifyIdentity, bindings, serviceAuthorization }) {
  const base = new URL(runtimeBase);
  const local = ["127.0.0.1", "[::1]", "localhost"].includes(base.hostname);
  if ((base.protocol !== "https:" && !(base.protocol === "http:" && local)) ||
      base.username || base.password || base.search || base.hash) {
    throw new Error("Configure Nori with an HTTPS runtime URL or a loopback HTTP runtime.");
  }
  if (!base.pathname.endsWith("/")) base.pathname += "/";
  if (typeof verifyIdentity !== "function" || typeof serviceAuthorization !== "function") {
    throw new Error("Configure the host identity and Nori service credential hooks.");
  }

  const directory = new Map();
  const noriUsers = new Set();
  for (const binding of bindings) {
    const { noriUserId } = binding;
    if (!validPrincipal(binding) || typeof noriUserId !== "string" ||
        !(noriUserId === "owner" || uuid.test(noriUserId)) ||
        directory.has(principalKey(binding)) || noriUsers.has(noriUserId)) {
      throw new Error("Each verified host identity must map to a distinct provisioned Nori user.");
    }
    directory.set(principalKey(binding), noriUserId);
    noriUsers.add(noriUserId);
  }

  return async (request) => {
    const route = routeFor(request);
    if (!route) return error(404, "Route not found.");
    let identity;
    try {
      identity = await verifyIdentity(request);
    } catch {
      return error(503, "Host identity verification is unavailable.");
    }
    if (!validPrincipal(identity)) return error(401, "Sign in to use Nori.");
    const userId = directory.get(principalKey(identity));
    if (!userId) return error(403, "This account is not provisioned for Nori.");
    if (route.invalidCursor) return error(400, "Invalid event cursor.");

    let body;
    if (route.mutation) {
      if (mediaType(request) !== "application/json") {
        return error(415, "Send application/json.");
      }
      body = await readInput(request);
      if (!validMutation(route.mutation, body)) return error(400, "Invalid Nori request.");
    }

    const target = new URL(`users/${userId}${route.path}`, base);
    const signal = AbortSignal.any([request.signal, AbortSignal.timeout(route.stream ? 170_000 : 12_000)]);
    try {
      const authorization = await serviceAuthorization(signal);
      if (!authorization) return error(503, "Nori service authentication is unavailable.");
      const headers = { Authorization: authorization, Accept: route.stream ? "text/event-stream" : "application/json" };
      if (body) headers["Content-Type"] = "application/json";
      const upstream = await fetch(target, {
        method: request.method, headers, signal, redirect: "error", cache: "no-store",
        body: body ? JSON.stringify(body) : undefined,
      });
      if (route.mutation) return await mutationResponse(upstream, route, body);
      return await relay(upstream, route.stream && upstream.ok ? "text/event-stream" : "application/json");
    } catch {
      return error(502, "Nori is unavailable. Retry with the same request ID if you submitted a change.");
    }
  };
}
