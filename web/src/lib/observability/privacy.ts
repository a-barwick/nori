import { getClient } from "@sentry/nextjs";
import type { Breadcrumb, ErrorEvent, EventHint, Log, Metric } from "@sentry/nextjs";

type Options = Parameters<typeof import("@sentry/nextjs").init>[0];
type Client = NonNullable<ReturnType<typeof import("@sentry/nextjs").init>>;
type StreamedSpan = Parameters<NonNullable<Options["beforeSendSpan"]>>[0];
type SamplingContext = Parameters<NonNullable<Options["tracesSampler"]>>[0];

export function protectEnvelopeHeaders(client: Client | undefined) {
  if (!client) return;
  // Sentry derives an envelope/baggage transaction name from the unsanitized
  // root span before beforeSendSpan runs. Remove it at both boundaries.
  client.on("createDsc", (dsc) => { delete dsc.transaction; });
  client.on("beforeEnvelope", (envelope) => {
    const trace = envelope[0].trace;
    if (trace && typeof trace === "object") {
      const incoming = trace as Record<string, unknown>;
      const safe: Record<string, string> = {};
      if (typeof incoming.trace_id === "string" && /^[0-9a-f]{32}$/.test(incoming.trace_id)) safe.trace_id = incoming.trace_id;
      const publicKey = client.getDsn()?.publicKey;
      if (publicKey) safe.public_key = publicKey;
      if (typeof incoming.sampled === "string" && ["true", "false"].includes(incoming.sampled)) safe.sampled = incoming.sampled;
      for (const key of ["sample_rate", "sample_rand"]) {
        const value = incoming[key];
        if (typeof value === "string" && value.length <= 20 && value.trim() && Number.isFinite(Number(value)) && Number(value) >= 0 && Number(value) <= 1) safe[key] = value;
      }
      const { environment, release } = client.getOptions();
      if (environment) safe.environment = environment;
      if (release) safe.release = release;
      envelope[0].trace = safe;
    }
    // Scope attributes are merged into logs and metrics after their individual
    // callbacks, so filter the serialized items at the final send boundary too.
    for (const [header, payload] of envelope[1]) {
      if ((header.type !== "log" && header.type !== "trace_metric") || !payload || typeof payload !== "object" || !("items" in payload) || !Array.isArray(payload.items)) continue;
      for (const item of payload.items) {
        if (!item || typeof item !== "object") continue;
        const record = item as Record<string, unknown>;
        const attributes = record.attributes && typeof record.attributes === "object" ? record.attributes as Record<string, { value?: unknown }> : {};
        const values = Object.fromEntries(Object.entries(attributes).map(([key, value]) => [key, value?.value]));
        const safe: Record<string, { type: string; value: unknown }> = {};
        for (const [key, value] of Object.entries(safeAttributes(values))) {
          safe[key] = { type: typeof value === "string" ? "string" : "integer", value };
        }
        if (typeof values["sentry.trace.parent_span_id"] === "string" && /^[0-9a-f]{16}$/.test(values["sentry.trace.parent_span_id"])) safe["sentry.trace.parent_span_id"] = { type: "string", value: values["sentry.trace.parent_span_id"] };
        if (Number.isSafeInteger(values["sentry.timestamp.sequence"])) safe["sentry.timestamp.sequence"] = { type: "integer", value: values["sentry.timestamp.sequence"] };
        const { environment, release } = client.getOptions();
        if (environment) safe["sentry.environment"] = { type: "string", value: environment };
        if (release) safe["sentry.release"] = { type: "string", value: release };
        record.attributes = safe;
      }
    }
  });
}

// SDK 11 collects these categories by default, even without sendDefaultPii.
export const dataCollection = {
  userInfo: false,
  cookies: false,
  httpHeaders: false,
  httpBodies: [],
  urlQueryParams: false,
  genAI: { inputs: false, outputs: false },
  mcp: { inputs: false, outputs: false },
  graphQL: { document: false, variables: false },
  databaseQueryData: false,
  queues: false,
  stackFrameVariables: false,
  frameContextLines: 0,
} satisfies NonNullable<Options["dataCollection"]>;

// Per-process/tab burst protection; billing limits remain account-side.
const limits = new Map<string, { start: number; used: number; dropped: number }>();
function allow(kind: string, maximum: number): boolean {
 const now = Date.now();
 let window = limits.get(kind);
 if (!window || now - window.start >= 3_600_000) { window = { start: now, used: 0, dropped: 0 }; limits.set(kind, window); }
 if (window.used++ < maximum) return true;
 window.dropped++;
 if (window.dropped === 1 || window.dropped % 100 === 0) console.warn(`Nori telemetry ${kind} rate limited (${window.dropped} dropped)`);
 return false;
}

const operations = new Set(["session", "turn", "input", "cancel", "command", "artifact", "trace"]);
const safeAttributes = (attributes: Record<string, unknown> = {}) => {
  const safe: Record<string, string | number> = {};
  if (typeof attributes.operation === "string" && operations.has(attributes.operation)) safe.operation = attributes.operation;
  if (typeof attributes.status === "number" && attributes.status >= 100 && attributes.status <= 599) safe.status = attributes.status;
  if (attributes.mode === "local" || attributes.mode === "hosted") safe.mode = attributes.mode;
  if (["reply", "error", "timeout", "unobserved"].includes(String(attributes.outcome))) safe.outcome = String(attributes.outcome);
  return safe;
};

export function sampleRate(value?: string): number {
  if (!value?.trim()) return 0.05;
  const rate = Number(value);
  return Number.isFinite(rate) && rate >= 0 && rate <= 1 ? rate : 0.05;
}

export function sampler(rate: number) {
  return (context: SamplingContext) => {
    const candidates = [context.normalizedRequest?.url, context.name,
      context.attributes["http.target"], context.attributes["url.path"], context.attributes["url.full"]];
    if (candidates.some((value) => typeof value === "string" && /\/api\/(?:agent\/)?(?:health|session|observability)(?:[/?#\s]|$)/.test(value))) return 0;
    return context.inheritOrSampleWith(rate);
  };
}

export function beforeBreadcrumb(crumb: Breadcrumb): Breadcrumb | null {
  if (crumb.category !== "nori" || !["turn submitted", "reply received"].includes(crumb.message ?? "")) return null;
  return { category: "nori", message: crumb.message, timestamp: crumb.timestamp, level: crumb.level };
}

export function beforeSend(event: ErrorEvent, hint: EventHint): ErrorEvent | null {
  if (!allow("errors", 50)) return null;
  // Exceptions can contain arbitrary server responses or user content. Preserve
  // the stack for diagnosis, but never send exception text or request details.
  delete event.message;
  delete event.logentry;
  delete event.user;
  delete event.extra;
  delete event.server_name;
  delete event.threads;
  delete event.modules;
  delete event.fingerprint;
  event.request = event.request?.method && ["GET", "POST"].includes(event.request.method) ? { method: event.request.method } : undefined;
  const trace = event.contexts?.trace;
  event.contexts = trace ? { trace: { trace_id: trace.trace_id, span_id: trace.span_id, parent_span_id: trace.parent_span_id } } : undefined;
  event.tags = safeAttributes(event.tags);
  event.transaction = event.transaction ? safeSpanName(event.transaction) : undefined;
  event.breadcrumbs = event.breadcrumbs?.map(beforeBreadcrumb).filter((crumb): crumb is Breadcrumb => crumb !== null);
  for (const exception of event.exception?.values ?? []) {
    exception.value = "Details withheld";
    if (!["Error", "TypeError", "RangeError", "ReferenceError", "SyntaxError", "URIError", "EvalError"].includes(exception.type ?? "")) exception.type = "Error";
    delete exception.module;
    if (exception.mechanism) { delete exception.mechanism.data; delete exception.mechanism.source; }
    for (const frame of exception.stacktrace?.frames ?? []) {
      delete frame.vars;
      delete frame.context_line;
      delete frame.pre_context;
      delete frame.post_context;
      delete frame.function;
      delete frame.module;
      frame.filename = safeFramePath(frame.filename);
      frame.abs_path = safeFramePath(frame.abs_path);
      delete frame.module_metadata;
    }
  }
  // The SDK builds debug images before beforeSend from the original frame
  // paths. Keep them aligned with the sanitized frames for source maps.
  if (event.debug_meta?.images) {
    event.debug_meta.images = event.debug_meta.images.flatMap((image) => {
      if (image.type !== "sourcemap" || !/^[0-9a-f-]{36}$/i.test(image.debug_id)) return [];
      const codeFile = safeFramePath(image.code_file);
      return codeFile ? [{ type: "sourcemap" as const, code_file: codeFile, debug_id: image.debug_id }] : [];
    });
  }
  hint.attachments = [];
  return event;
}

function safeFramePath(path?: string): string | undefined {
  if (!path) return undefined;
  const clean = path.split(/[?#]/, 1)[0];
  // Stack frames should point to application bundles, never user-provided URLs.
  if (/^https?:\/\/[^/@\s]+\/_next\/static\/[a-z0-9_./-]+\.m?js$/i.test(clean)) return clean;
  const bundled = clean.match(/\/(?:_next\/static|\.next\/server)\/[a-z0-9_./-]+\.m?js$/i);
  if (bundled) return bundled[0];
  if (/^webpack(?:-internal)?:\/\/[a-z0-9_./()\[\]@+-]+\.(?:m?js|tsx?)$/i.test(clean)) return clean;
  return undefined;
}

function safeSpanName(name: string): string {
  const route = name.match(/^(?:(GET|POST) )?(\/(?:api\/(?:agent\/)?(?:session|turn|input|cancel|command|artifact|trace|health|\[operation\])|goals|ideas|library|settings)?)(?:[?#].*)?$/);
  if (route) return `${route[1] ? `${route[1]} ` : ""}${route[2]}`;
  if (name === "nori.submit_to_reply") return name;
  return "operation";
}

export function beforeSendSpan(span: StreamedSpan): StreamedSpan {
  const attrs: Record<string, unknown> = {};
  const options = getClient()?.getOptions();
  for (const [key, value] of Object.entries(span.attributes)) {
    if (["sentry.op", "sentry.origin", "sentry.kind", "sentry.segment.name.source"].includes(key) &&
        typeof value === "string" && /^[a-z0-9_.-]{1,100}$/i.test(value)) attrs[key] = value;
    else if (["sentry.segment.name", "sentry.transaction", "http.route"].includes(key) && typeof value === "string") attrs[key] = safeSpanName(value);
    else if (key === "sentry.segment.id" && typeof value === "string" && /^[0-9a-f]{16}$/.test(value)) attrs[key] = value;
    else if (key === "sentry.environment" && value === options?.environment) attrs[key] = value;
    else if (key === "sentry.release" && value === options?.release) attrs[key] = value;
    else if (key === "sentry.trace.lifecycle" && value === "stream") attrs[key] = value;
    else if (["sentry.sample_rate", "http.response.status_code", "http.status_code", "sentry.exclusive_time"].includes(key) && typeof value === "number") attrs[key] = value;
    else if (/^browser\.web_vital\.[a-z]+\.(?:value|load_time|render_time|size|request_time)$/.test(key) && typeof value === "number") attrs[key] = value;
    else if (key === "nori.mode" && (value === "local" || value === "hosted")) attrs[key] = value;
    else if (key === "nori.outcome" && (value === "reply" || value === "error" || value === "timeout" || value === "unobserved")) attrs[key] = value;
  }
  return { ...span, name: safeSpanName(span.name), attributes: attrs, links: undefined };
}

export function beforeSendLog(log: Log): Log | null {
  if (log.message !== "nori.runtime.unavailable" && log.message !== "nori.runtime.server_error") return null;
  if (!allow("logs", 100)) return null;
  return { ...log, attributes: safeAttributes(log.attributes) };
}

export function beforeSendMetric(metric: Metric): Metric | null {
  if (!["nori.submit_to_reply", "nori.submit_to_terminal", "nori.submissions", "nori.reply.outcomes"].includes(metric.name) || !allow("metrics", 200)) return null;
  return { ...metric, attributes: safeAttributes(metric.attributes) };
}
