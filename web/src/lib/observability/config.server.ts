import "server-only";

export function browserObservabilityConfig() {
  return {
    dsn: process.env.SENTRY_WEB_DSN || process.env.NEXT_PUBLIC_SENTRY_DSN || "",
    environment: process.env.SENTRY_ENVIRONMENT || process.env.NEXT_PUBLIC_SENTRY_ENVIRONMENT || process.env.NODE_ENV,
    release: process.env.SENTRY_RELEASE || process.env.NEXT_PUBLIC_SENTRY_RELEASE || "",
    tracesSampleRate: process.env.SENTRY_TRACES_SAMPLE_RATE || process.env.NEXT_PUBLIC_SENTRY_TRACES_SAMPLE_RATE || "",
  };
}
