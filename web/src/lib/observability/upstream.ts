import * as Sentry from "@sentry/nextjs";
import { isRuntimeMethod } from "@/lib/runtime/methods";

// Build a fresh trace context from the current server span. Do not forward
// browser/IAP headers or arbitrary baggage to the runtime.
export function upstreamTraceHeaders(request: Request): Record<string, string> {
  const span = Sentry.getClient() ? Sentry.getActiveSpan() : undefined;
  if (span) {
    const context = span.spanContext();
    if (/^[0-9a-f]{32}$/.test(context.traceId) && !/^0+$/.test(context.traceId) &&
        /^[0-9a-f]{16}$/.test(context.spanId) && !/^0+$/.test(context.spanId)) {
      return {
        traceparent: `00-${context.traceId}-${context.spanId}-${context.traceFlags & 1 ? "01" : "00"}`,
        "sentry-trace": Sentry.spanToTraceHeader(span),
      };
    }
  }
  // Sentry can be disabled on the web server while a same-origin browser is
  // tracing. Preserve a valid W3C parent without copying any other headers.
  const parent = request.headers.get("traceparent") ?? "";
  const match = /^00-([0-9a-f]{32})-([0-9a-f]{16})-(0[01])$/.exec(parent);
  if (match && !/^0+$/.test(match[1]) && !/^0+$/.test(match[2])) return { traceparent: parent };
  // Idle session polls have no sampled span. Still give the runtime a fresh,
  // unsampled correlation ID so a rejection can be joined to its logs.
  return { traceparent: `00-${crypto.randomUUID().replaceAll("-", "")}-${crypto.randomUUID().replaceAll("-", "").slice(0, 16)}-00` };
}

export function rejectionHeaders(headers: Headers): Record<string, string> {
  const safe: Record<string, string> = {
    "X-Nori-Rejection-Source": headers.get("x-nori-rejection-source") === "runtime" ? "runtime" : "upstream",
  };
  const retry = headers.get("retry-after") ?? "";
  // Only bounded delay-seconds, never arbitrary header text.
  if (/^\d{1,6}$/.test(retry)) safe["Retry-After"] = retry;
  return safe;
}

let lastRejection = 0;
function cloudTraceID(headers: Headers) {
  const id = /^([0-9a-f]{32})\/[0-9]{1,20}(?:;o=[01])?$/.exec(headers.get("x-cloud-trace-context") ?? "")?.[1];
  return id && !/^0+$/.test(id) ? id : undefined;
}

export function reportUpstreamRejection(operation: string, request: Request, sent: Record<string, string>, received: Headers) {
  if (Date.now() - lastRejection < 60_000) return;
  lastRejection = Date.now();
  const safe = rejectionHeaders(received);
  const cloudTrace = cloudTraceID(request.headers);
  const upstreamTrace = cloudTraceID(received);
  const traceID = /^00-([0-9a-f]{32})-[0-9a-f]{16}-0[01]$/.exec(sent.traceparent ?? "")?.[1];
  // Structured stderr remains useful when Sentry is disabled or drops a poll.
  // No URL, body, identity, or arbitrary request/response header is exported.
  try {
    console.warn(JSON.stringify({ severity: "WARNING", message: "nori.runtime.rate_limited",
      operation: isRuntimeMethod(operation) ? operation : "unknown",
      status: 429, source: safe["X-Nori-Rejection-Source"],
      ...(traceID ? { runtime_trace_id: traceID } : {}),
      ...(cloudTrace ? { web_trace_id: cloudTrace } : {}),
      ...(upstreamTrace ? { upstream_trace_id: upstreamTrace } : {}),
      ...(safe["Retry-After"] ? { retry_after_seconds: Number(safe["Retry-After"]) } : {}),
    }));
  } catch { /* Diagnostics must not change the rejection response. */ }
}

let lastPollFailure = 0;

export function reportUpstreamFailure(operation: string, mode: "local" | "hosted", status?: number) {
  // A failed idle refresh may be the only evidence of a runtime outage. A stream retries by itself, so it can repeat.
  if (operation === "v1/snapshot" || operation === "v1/events") {
    if (Date.now() - lastPollFailure < 60_000) return;
    lastPollFailure = Date.now();
  }
  const attributes = { operation, mode, ...(status === undefined ? {} : { status }) };
  try {
    Sentry.logger.error(status === undefined ? "nori.runtime.unavailable" : "nori.runtime.server_error", attributes);
    Sentry.withScope((scope) => {
      scope.setTags(attributes);
      Sentry.captureException(new Error(status === undefined ? "Runtime connection failed" : "Runtime returned a server error"));
    });
  } catch { /* Telemetry failure must not change the runtime response. */ }
}
