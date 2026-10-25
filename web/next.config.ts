import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  cacheComponents: true,
  partialPrefetching: true,
  // The dev badge would sit on the rail's menu button.
  devIndicators: false,
  // Keep next dev from writing its own AGENTS.md.
  agentRules: false,
  experimental: {
    // The shell renders on the client (clock, viewport, saved state), so
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
