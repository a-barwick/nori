import * as Sentry from "@sentry/nextjs";
import { beforeBreadcrumb, beforeSend, beforeSendLog, beforeSendMetric, beforeSendSpan, dataCollection, protectEnvelopeHeaders, sampleRate, sampler } from "@/lib/observability/privacy";

const dsn = process.env.SENTRY_WEB_DSN || process.env.NEXT_PUBLIC_SENTRY_DSN;
if (dsn) {
  const client = Sentry.init({
    dsn,
    environment: process.env.SENTRY_ENVIRONMENT || process.env.NODE_ENV,
    release: process.env.SENTRY_RELEASE,
    dataCollection,
    tracesSampler: sampler(sampleRate(process.env.SENTRY_TRACES_SAMPLE_RATE)),
    tracePropagationTargets: [],
    beforeBreadcrumb, beforeSend, beforeSendSpan, beforeSendLog, beforeSendMetric,
  });
  protectEnvelopeHeaders(client);
}
