import assert from "node:assert/strict";
import { afterEach, describe, test } from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { securityHeaders } from "../../next.config";
import PrivacyPage from "@/app/privacy/page";
import TermsPage from "@/app/terms/page";
import SignedOut from "@/app/signed-out/page";
import {
  API_DEFAULT_CSP,
  BASELINE_SECURITY_HEADERS,
  buildAppCsp,
  HSTS_HEADER_VALUE,
  isLoopbackHost,
  shouldSetHsts,
} from "./securityHeaders";

const env = process.env as Record<string, string | undefined>;

describe("securityHeaders helpers and Next.js header rules", () => {
  const originalNodeEnv = env.NODE_ENV;

  afterEach(() => {
    if (originalNodeEnv === undefined) delete env.NODE_ENV;
    else env.NODE_ENV = originalNodeEnv;
  });

  test("isLoopbackHost and shouldSetHsts restrict HSTS to production non-loopback HTTPS", () => {
    assert.equal(isLoopbackHost("localhost"), true);
    assert.equal(isLoopbackHost("localhost:3000"), true);
    assert.equal(isLoopbackHost("127.0.0.1:3210"), true);
    assert.equal(isLoopbackHost("[::1]:3000"), true);
    assert.equal(isLoopbackHost("app.nori.example"), false);

    assert.equal(
      shouldSetHsts({ url: "https://app.nori.example/work", headers: new Headers() }, "production"),
      true,
    );
    assert.equal(
      shouldSetHsts(
        { url: "http://app.nori.example/work", headers: new Headers({ "x-forwarded-proto": "https" }) },
        "production",
      ),
      true,
    );
    assert.equal(
      shouldSetHsts({ url: "http://127.0.0.1:3210/work", headers: new Headers() }, "production"),
      false,
    );
    assert.equal(
      shouldSetHsts(
        { url: "http://127.0.0.1:3210/work", headers: new Headers({ "x-forwarded-proto": "https", host: "127.0.0.1:3210" }) },
        "production",
      ),
      false,
    );
    assert.equal(
      shouldSetHsts({ url: "https://app.nori.example/work", headers: new Headers() }, "development"),
      false,
    );
  });

  test("buildAppCsp includes required directives without unsafe-inline for scripts", () => {
    const prodCsp = buildAppCsp("abc123", { isDev: false });
    assert.ok(prodCsp.includes("default-src 'self'"));
    assert.ok(prodCsp.includes("script-src 'self' 'nonce-abc123'"));
    assert.ok(!prodCsp.includes("script-src 'self' 'unsafe-inline'"));
    assert.ok(!prodCsp.includes("'unsafe-eval'"));
    assert.ok(prodCsp.includes("style-src 'self' 'unsafe-inline'"));
    assert.ok(prodCsp.includes("img-src 'self' data: blob:"));
    assert.ok(prodCsp.includes("font-src 'self'"));
    assert.ok(prodCsp.includes("connect-src 'self'"));
    assert.ok(prodCsp.includes("frame-ancestors 'none'"));
    assert.ok(prodCsp.includes("base-uri 'self'"));
    assert.ok(prodCsp.includes("form-action 'self'"));
    assert.ok(prodCsp.includes("object-src 'none'"));
  });

  test("next.config.ts securityHeaders applies baseline headers globally and preserves download sandbox CSP", async () => {
    env.NODE_ENV = "production";
    const rules = await securityHeaders();

    const globalRule = rules.find((rule) => rule.source === "/:path*" && !rule.has);
    assert.ok(globalRule, "expected global baseline header rule");
    assert.deepEqual(globalRule.headers, [...BASELINE_SECURITY_HEADERS]);

    const apiCspRule = rules.find((rule) =>
      rule.headers.some((header) => header.key === "Content-Security-Policy" && header.value === API_DEFAULT_CSP),
    );
    assert.ok(apiCspRule, "expected API default CSP rule");
    const apiPattern = new RegExp(`^${apiCspRule.source}$`);
    for (const apiPath of ["/api/health", "/api/v1/snapshot", "/api/turn", "/api/auth/sign-in", "/api/v1/uploads"]) {
      assert.equal(apiPattern.test(apiPath), true, `expected ${apiPath} to receive default API CSP`);
    }
    for (const downloadPath of [
      "/api/v1/uploads/file-123",
      "/api/v1/results/res-123/file",
      "/api/v1/library/doc-123/file",
    ]) {
      assert.equal(
        apiPattern.test(downloadPath),
        false,
        `expected ${downloadPath} to be excluded so upstream sandbox CSP is preserved`,
      );
    }

    const hstsRule = rules.find((rule) =>
      rule.headers.some((header) => header.key === "Strict-Transport-Security" && header.value === HSTS_HEADER_VALUE),
    );
    assert.ok(hstsRule, "expected production HSTS rule");
    assert.deepEqual(hstsRule.has, [{ type: "header", key: "x-forwarded-proto", value: "https" }]);
    assert.ok(hstsRule.missing?.some((entry) => entry.type === "host"));
  });
});

describe("public legal pages and signed-out legal links", () => {
  test("Privacy Policy documents collected data, BYOK KMS encryption, providers, telemetry, retention, and deletion", () => {
    const html = renderToStaticMarkup(React.createElement(PrivacyPage));
    assert.match(html, /Privacy Policy/);
    assert.match(html, /email address/i);
    assert.match(html, /display name/i);
    assert.match(html, /timezone/i);
    assert.match(html, /Cloud KMS/i);
    assert.match(html, /Sentry/i);
    assert.match(html, /Arize/i);
    assert.match(html, /redacted/i);
    assert.match(html, /Reset data/i);
    assert.match(html, /delete your Nori account/i);
    assert.match(html, /href="\/terms"/);
  });

  test("Terms of Service renders cleanly and links to Privacy Policy", () => {
    const html = renderToStaticMarkup(React.createElement(TermsPage));
    assert.match(html, /Terms of Service/);
    assert.match(html, /BYOK/);
    assert.match(html, /Reset data/i);
    assert.match(html, /href="\/privacy"/);
  });

  test("SignedOut page links to Privacy Policy and Terms of Service", () => {
    const html = renderToStaticMarkup(React.createElement(SignedOut));
    assert.match(html, /href="\/privacy"/);
    assert.match(html, /href="\/terms"/);
  });
});
