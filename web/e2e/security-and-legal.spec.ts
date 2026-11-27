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

test("/privacy and /terms redirect to the public site without signing in", async ({ request }) => {
  for (const pathname of ["/privacy", "/terms"]) {
    const response = await request.get(pathname, { maxRedirects: 0 });
    expect(response.status()).toBe(307);
    expect(response.headers()["location"]).toBe(`https://www.noribot.chat${pathname}`);
  }
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

  const accountResponse = await page.goto(signInURL("second", "/settings/account"));
  expect(accountResponse?.status()).toBe(200);
  assertBaselineHeaders(accountResponse!.headers());
  await expect(page.getByRole("link", { name: "Privacy Policy" })).toHaveAttribute("href", "https://www.noribot.chat/privacy");
  await expect(page.getByRole("link", { name: "Terms of Service" })).toHaveAttribute("href", "https://www.noribot.chat/terms");

  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/signed-out$/);
  await expect(page.getByRole("button", { name: "Sign in with Google" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Privacy Policy" })).toHaveAttribute("href", "https://www.noribot.chat/privacy");
  await expect(page.getByRole("link", { name: "Terms of Service" })).toHaveAttribute("href", "https://www.noribot.chat/terms");

  expect(cspViolations).toEqual([]);
  expect(pageErrors).toEqual([]);
});
