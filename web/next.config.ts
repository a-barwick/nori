import type { NextConfig } from "next";
import { withSentryConfig } from "@sentry/nextjs/config";

const nextConfig: NextConfig = {
  output: "standalone",
  cacheComponents: true,
  partialPrefetching: true,
  // Keep the development badge clear of chat controls.
  devIndicators: false,
  // Keep next dev from writing its own AGENTS.md.
  agentRules: false,
  experimental: {
    // Chat renders on the client (clock and viewport), so
    // per-page instant-navigation insights only apply where a page opts in.
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
  tunnelRoute: "/monitoring",
  org: process.env.SENTRY_ORG,
  project: process.env.SENTRY_PROJECT,
  authToken: process.env.SENTRY_AUTH_TOKEN,
  release: { name: process.env.SENTRY_RELEASE },
  sourcemaps: { disable: !(process.env.SENTRY_AUTH_TOKEN && process.env.SENTRY_ORG && process.env.SENTRY_PROJECT) },
  silent: !process.env.CI,
  errorHandler: () => { throw new Error("Sentry source-map upload failed; refusing an unsymbolicated release."); },
});
