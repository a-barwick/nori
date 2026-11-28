import * as Sentry from "@sentry/nextjs";
import { ignoredRequestSpans, privacyOptions, protectEnvelopeHeaders } from "@/lib/observability/privacy";

type BrowserConfig = { dsn?: string; environment?: string; release?: string; tracesSampleRate?: string };

const earlyErrors: Error[] = [];
function rememberEarlyError(value: unknown) {
  if (earlyErrors.length >= 10) return;
  const error = new Error("Early browser error");
  if (value instanceof Error) {
    try {
      if (["Error", "TypeError", "RangeError", "ReferenceError", "SyntaxError", "URIError", "EvalError"].includes(value.name)) error.name = value.name;
      if (value.stack) error.stack = `${error.name}: Early browser error\n${value.stack.split("\n").slice(1, 31).join("\n").slice(0, 16_000)}`;
    } catch {  }
  }
  earlyErrors.push(error);
}
const onEarlyError = (event: ErrorEvent) => rememberEarlyError(event.error);
const onEarlyRejection = (event: PromiseRejectionEvent) => rememberEarlyError(event.reason);
window.addEventListener("error", onEarlyError);
window.addEventListener("unhandledrejection", onEarlyRejection);

async function initialize() {
  let config: BrowserConfig = {
    dsn: process.env.NEXT_PUBLIC_SENTRY_DSN,
    environment: process.env.NEXT_PUBLIC_SENTRY_ENVIRONMENT || process.env.NODE_ENV,
    release: process.env.NEXT_PUBLIC_SENTRY_RELEASE,
    tracesSampleRate: process.env.NEXT_PUBLIC_SENTRY_TRACES_SAMPLE_RATE,
  };
  let documentConfig: unknown;
  try { documentConfig = JSON.parse(document.documentElement.getAttribute("data-nori-observability") || "null"); }
  catch {  }
  if (documentConfig && typeof documentConfig === "object" && "dsn" in documentConfig && typeof documentConfig.dsn === "string") {
    config = documentConfig as BrowserConfig;
  } else {
    try {
      const response = await fetch("/api/observability", { cache: "no-store", signal: AbortSignal.timeout(10_000) });
      if (response.ok) config = await response.json() as BrowserConfig;
    } catch {  }
  }
  let initialized = false;
  try {
    if (!config.dsn) return;
    const client = Sentry.init({
      dsn: config.dsn,
      tunnel: "/sentry-tunnel",
      environment: config.environment,
      release: config.release,
      transportOptions: { fetchOptions: { credentials: "same-origin" } },
      ...privacyOptions(config.tracesSampleRate),
      tracePropagationTargets: [/^\/api\/v1\//, /^\/api\/(?:input|cancel|command|artifact|trace)$/],
      propagateTraceparent: true,
      ignoreSpans: ignoredRequestSpans,
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
