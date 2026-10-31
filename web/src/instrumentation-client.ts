import * as Sentry from "@sentry/nextjs";
import { beforeBreadcrumb, beforeSend, beforeSendLog, beforeSendMetric, beforeSendSpan, dataCollection, protectEnvelopeHeaders, sampleRate, sampler } from "@/lib/observability/privacy";

type BrowserConfig = { dsn?: string; environment?: string; release?: string; tracesSampleRate?: string };

// Capture early failures while the non-blocking runtime-config request is in
// flight. The queue is bounded and is replayed only after privacy filters exist.
const earlyErrors: Error[] = [];
function rememberEarlyError(value: unknown) {
  if (earlyErrors.length >= 10) return;
  const error = new Error("Early browser error");
  if (value instanceof Error) {
    try {
      if (["Error", "TypeError", "RangeError", "ReferenceError", "SyntaxError", "URIError", "EvalError"].includes(value.name)) error.name = value.name;
      if (value.stack) error.stack = `${error.name}: Early browser error\n${value.stack.split("\n").slice(1, 31).join("\n").slice(0, 16_000)}`;
    } catch { /* Keep the generic error if the original has unusual getters. */ }
  }
  earlyErrors.push(error);
}
const onEarlyError = (event: ErrorEvent) => rememberEarlyError(event.error);
const onEarlyRejection = (event: PromiseRejectionEvent) => rememberEarlyError(event.reason);
window.addEventListener("error", onEarlyError);
window.addEventListener("unhandledrejection", onEarlyRejection);

async function initialize() {
  // The same image can be promoted between environments. Runtime settings
  // are public DSN/configuration only and are served to the signed-in owner.
  let config: BrowserConfig = {
    dsn: process.env.NEXT_PUBLIC_SENTRY_DSN,
    environment: process.env.NEXT_PUBLIC_SENTRY_ENVIRONMENT || process.env.NODE_ENV,
    release: process.env.NEXT_PUBLIC_SENTRY_RELEASE,
    tracesSampleRate: process.env.NEXT_PUBLIC_SENTRY_TRACES_SAMPLE_RATE,
  };
  // The owner-gated document includes public runtime settings before this
  // script runs, so even the first submission gets a span and trace headers.
  // The root attribute is parsed before any async Next scripts can execute.
  // Keep the non-blocking fetch fallback for older documents.
  let documentConfig: unknown;
  try { documentConfig = JSON.parse(document.documentElement.getAttribute("data-nori-observability") || "null"); }
  catch { /* Fall back if an older document contains invalid metadata. */ }
  if (documentConfig && typeof documentConfig === "object" && "dsn" in documentConfig && typeof documentConfig.dsn === "string") {
    config = documentConfig as BrowserConfig;
  } else {
    try {
      const response = await fetch("/api/observability", { cache: "no-store", signal: AbortSignal.timeout(10_000) });
      if (response.ok) config = await response.json() as BrowserConfig;
    } catch { /* Observability must not prevent the app from loading. */ }
  }
  let initialized = false;
  try {
    if (!config.dsn) return;
    const client = Sentry.init({
      dsn: config.dsn,
      environment: config.environment,
      release: config.release,
      // The owner-protected first-party tunnel needs the app's IAP cookie.
      // Its route forwards only the envelope, never these request headers.
      transportOptions: { fetchOptions: { credentials: "same-origin" } },
      dataCollection,
      tracesSampler: sampler(sampleRate(config.tracesSampleRate)),
      // Turn requests carry the explicit submit span. Automatic propagation
      // would otherwise attach baggage from a different active browser span.
      tracePropagationTargets: [/^\/api\/(?:agent\/)?(?:input|cancel|command|artifact|trace)$/],
      propagateTraceparent: true,
      ignoreSpans: [/\/api\/(?:agent\/)?(?:health|session|observability)(?:[/?#\s]|$)/],
      beforeBreadcrumb, beforeSend, beforeSendSpan, beforeSendLog, beforeSendMetric,
    });
    protectEnvelopeHeaders(client);
    initialized = !!client;
  } finally {
    window.removeEventListener("error", onEarlyError);
    window.removeEventListener("unhandledrejection", onEarlyRejection);
    for (const error of earlyErrors.splice(0)) if (initialized) Sentry.captureException(error);
  }
}

void initialize().catch(() => undefined);

export const onRouterTransitionStart = Sentry.captureRouterTransitionStart;
