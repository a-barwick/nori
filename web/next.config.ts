import type { NextConfig } from "next";

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

export default nextConfig;
