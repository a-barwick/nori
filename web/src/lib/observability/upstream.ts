import * as Sentry from "@sentry/nextjs";

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
  return match && !/^0+$/.test(match[1]) && !/^0+$/.test(match[2]) ? { traceparent: parent } : {};
}

export function reportUpstreamFailure(operation: string, mode: "local" | "hosted", status?: number) {
  // Session refreshes may run frequently; report the initiating operation
  // instead of producing a stream of repeated polling failures.
  if (operation === "session") return;
  const attributes = { operation, mode, ...(status === undefined ? {} : { status }) };
  try {
    Sentry.logger.error(status === undefined ? "nori.runtime.unavailable" : "nori.runtime.server_error", attributes);
    Sentry.withScope((scope) => {
      scope.setTags(attributes);
      Sentry.captureException(new Error(status === undefined ? "Runtime connection failed" : "Runtime returned a server error"));
    });
  } catch { /* Telemetry failure must not change the runtime response. */ }
}
