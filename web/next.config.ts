import type { NextConfig } from "next";
import { withSentryConfig } from "@sentry/nextjs/config";
import { API_DEFAULT_CSP, BASELINE_SECURITY_HEADERS, HSTS_HEADER_VALUE } from "./src/lib/securityHeaders";

export async function securityHeaders() {
  const rules: Awaited<ReturnType<NonNullable<NextConfig["headers"]>>> = [
    {
      source: "/:path*",
      headers: [...BASELINE_SECURITY_HEADERS],
    },
    {
      source: "/api/((?!v1/uploads/|v1/results/|v1/library/).*)",
      headers: [{ key: "Content-Security-Policy", value: API_DEFAULT_CSP }],
    },
  ];
  if (process.env.NODE_ENV === "production") {
    rules.push({
      source: "/:path*",
      has: [{ type: "header", key: "x-forwarded-proto", value: "https" }],
      missing: [{ type: "host", value: "(localhost|127\\.0\\.0\\.1|\\[::1\\])(:\\d+)?" }],
      headers: [{ key: "Strict-Transport-Security", value: HSTS_HEADER_VALUE }],
    });
  }
  return rules;
}

const nextConfig: NextConfig = {
  output: "standalone",
  cacheComponents: true,
  partialPrefetching: true,
  devIndicators: false,
  allowedDevOrigins: process.env.NORI_DEV_ORIGINS?.split(",").map((origin) => origin.trim()).filter(Boolean),
  agentRules: false,
  headers: securityHeaders,
  experimental: {
    instantInsights: { validationLevel: "manual-warning" },
  },
  turbopack: {
    root: __dirname,
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
};

const uploadConfigured = !!(process.env.SENTRY_AUTH_TOKEN || process.env.SENTRY_ORG || process.env.SENTRY_PROJECT);
if (uploadConfigured && !(process.env.SENTRY_AUTH_TOKEN && process.env.SENTRY_ORG && process.env.SENTRY_PROJECT && process.env.SENTRY_RELEASE)) {
  throw new Error("Source-map upload requires SENTRY_AUTH_TOKEN, SENTRY_ORG, SENTRY_PROJECT, and SENTRY_RELEASE together.");
}
export default withSentryConfig(nextConfig, {
  tunnelRoute: "/sentry-tunnel",
  org: process.env.SENTRY_ORG,
  project: process.env.SENTRY_PROJECT,
  authToken: process.env.SENTRY_AUTH_TOKEN,
  release: { name: process.env.SENTRY_RELEASE },
  sourcemaps: { disable: !(process.env.SENTRY_AUTH_TOKEN && process.env.SENTRY_ORG && process.env.SENTRY_PROJECT) },
  silent: !process.env.CI,
  errorHandler: () => { throw new Error("Sentry source-map upload failed; refusing an unsymbolicated release."); },
});
