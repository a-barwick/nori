import { expect, test } from "@playwright/test";

const app = "http://127.0.0.1:4317";
function signInURL(account: string, path = "/") {
  return `http://127.0.0.1:9445/sign-in?account=${account}&to=${encodeURIComponent(app + path)}`;
}

function assertBaselineHeaders(headers: Record<string, string>) {
  expect(headers["x-content-type-options"]).toBe("nosniff");
  expect(headers["x-frame-options"]).toBe("DENY");
  expect(headers["referrer-policy"]).toBe("strict-origin-when-cross-origin");
  expect(headers["permissions-policy"]).toBe("camera=(), microphone=(), geolocation=(), interest-cohort=()");
  expect(headers["strict-transport-security"]).toBeUndefined();
}

test("unauthenticated /privacy and /terms return 200 with security headers and zero CSP violations", async ({ page }) => {
  const cspViolations: string[] = [];
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error" && /content security policy/i.test(message.text())) {
      cspViolations.push(message.text());
    }
  });
  await page.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (event) => {
      console.error(`Content Security Policy violation: ${event.violatedDirective} ${event.blockedURI}`);
    });
  });

  for (const [pathname, heading] of [
    ["/privacy", "Privacy Policy"],
    ["/terms", "Terms of Service"],
  ] as const) {
    const response = await page.goto(pathname);
    expect(response?.status()).toBe(200);
    const headers = response!.headers();
    assertBaselineHeaders(headers);
    const csp = headers["content-security-policy"] ?? "";
    expect(csp).toContain("default-src 'self'");
    expect(csp).toMatch(/script-src 'self' 'nonce-[^']+'/);
    expect(csp).not.toMatch(/script-src[^;]*'unsafe-inline'/);
    await expect(page.getByRole("heading", { name: heading, level: 1 })).toBeVisible();
  }

  expect(cspViolations).toEqual([]);
  expect(pageErrors).toEqual([]);
});

test("authenticated app, Settings -> Account, /signed-out, and /api/health enforce headers with zero CSP violations", async ({ page, request }) => {
  const cspViolations: string[] = [];
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error" && /content security policy/i.test(message.text())) {
      cspViolations.push(message.text());
    }
  });
  await page.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (event) => {
      console.error(`Content Security Policy violation: ${event.violatedDirective} ${event.blockedURI}`);
    });
  });

  const health = await request.get("/api/health");
  expect(health.status()).toBe(200);
  assertBaselineHeaders(health.headers());
  expect(health.headers()["content-security-policy"]).toBe("default-src 'none'; frame-ancestors 'none'");

  const accountResponse = await page.goto(signInURL("first", "/settings/account"));
  expect(accountResponse?.status()).toBe(200);
  assertBaselineHeaders(accountResponse!.headers());
  await expect(page.getByRole("link", { name: "Privacy Policy" })).toHaveAttribute("href", "/privacy");
  await expect(page.getByRole("link", { name: "Terms of Service" })).toHaveAttribute("href", "/terms");

  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { name: "Signed out" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Privacy Policy" })).toHaveAttribute("href", "/privacy");
  await expect(page.getByRole("link", { name: "Terms of Service" })).toHaveAttribute("href", "/terms");

  await page.getByRole("link", { name: "Privacy Policy" }).click();
  await expect(page.getByRole("heading", { name: "Privacy Policy", level: 1 })).toBeVisible();

  expect(cspViolations).toEqual([]);
  expect(pageErrors).toEqual([]);
});
