import "server-only";
import * as Sentry from "@sentry/nextjs";
import { privacyOptions, protectEnvelopeHeaders } from "./privacy";

export function initServerObservability(ignoreSpans?: RegExp[]) {
  const dsn = process.env.SENTRY_WEB_DSN || process.env.NEXT_PUBLIC_SENTRY_DSN;
  if (!dsn) return;
  const client = Sentry.init({
    dsn,
    environment: process.env.SENTRY_ENVIRONMENT || process.env.NODE_ENV,
    release: process.env.SENTRY_RELEASE,
    ...privacyOptions(process.env.SENTRY_TRACES_SAMPLE_RATE),
    // The runtime relay creates sanitized upstream trace headers itself.
    tracePropagationTargets: [],
    ignoreSpans,
  });
  protectEnvelopeHeaders(client);
}

export function browserObservabilityConfig() {
  return {
    dsn: process.env.SENTRY_WEB_DSN || process.env.NEXT_PUBLIC_SENTRY_DSN || "",
    environment: process.env.SENTRY_ENVIRONMENT || process.env.NEXT_PUBLIC_SENTRY_ENVIRONMENT || process.env.NODE_ENV,
    release: process.env.SENTRY_RELEASE || process.env.NEXT_PUBLIC_SENTRY_RELEASE || "",
    tracesSampleRate: process.env.SENTRY_TRACES_SAMPLE_RATE || process.env.NEXT_PUBLIC_SENTRY_TRACES_SAMPLE_RATE || "",
  };
}
