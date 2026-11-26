import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { NextRequest } from "next/server";
import { POST as signIn } from "@/app/api/auth/sign-in/route";
import { POST as signOut } from "@/app/api/auth/sign-out/route";
import { proxy } from "@/proxy";
import { resolveUser } from "./auth";

describe("Web sign out", () => {
  test("signs out without contacting Core and blocks subsequent app and API requests", async () => {
    const savedOrigin = process.env.NORI_APP_ORIGIN;
    const savedProvider = process.env.NORI_AUTH_PROVIDER;
    const savedFetch = globalThis.fetch;
    process.env.NORI_APP_ORIGIN = "https://nori.example";
    process.env.NORI_AUTH_PROVIDER = "iap";
    globalThis.fetch = () => { throw new Error("Core must not be called"); };
    try {
      const response = await signOut(new Request("https://nori.example/api/auth/sign-out", {
        method: "POST", headers: { origin: "https://nori.example", "sec-fetch-site": "same-origin" },
      }));
      assert.equal(response.status, 303);
      assert.equal(response.headers.get("location"), "/signed-out?gcp-iap-mode=CLEAR_LOGIN_COOKIE");
      assert.match(response.headers.get("set-cookie") ?? "", /nori_signed_out=1;.*HttpOnly/);

      const headers = new Headers({ cookie: "nori_signed_out=1" });
      const apiIdentity = await resolveUser(headers);
      assert.equal("status" in apiIdentity && apiIdentity.status, 403);
      const page = await proxy(new NextRequest("https://nori.example/work", { headers }));
      assert.equal(page.status, 307);
      assert.equal(page.headers.get("location"), "https://nori.example/signed-out");
      const signedOutPage = await proxy(new NextRequest("https://nori.example/signed-out", { headers }));
      assert.equal(signedOutPage.status, 200);
      const directVisit = await proxy(new NextRequest("https://nori.example/signed-out"));
      assert.equal(directVisit.status, 307);
      assert.equal(directVisit.headers.get("location"), "https://nori.example/");

      const login = await signIn(new Request("https://nori.example/api/auth/sign-in", {
        method: "POST", headers: { origin: "https://nori.example", "sec-fetch-site": "same-origin" },
      }));
      assert.equal(login.status, 303);
      assert.match(login.headers.get("set-cookie") ?? "", /nori_signed_out=;.*Max-Age=0/);
      assert.equal(login.headers.get("location"), "/");
    } finally {
      globalThis.fetch = savedFetch;
      if (savedOrigin === undefined) delete process.env.NORI_APP_ORIGIN; else process.env.NORI_APP_ORIGIN = savedOrigin;
      if (savedProvider === undefined) delete process.env.NORI_AUTH_PROVIDER; else process.env.NORI_AUTH_PROVIDER = savedProvider;
    }
  });

  test("rejects cross-origin sign-in and sign-out", async () => {
    const request = () => new Request("https://nori.example/api/auth/sign-out", {
      method: "POST", headers: { origin: "https://other.example", host: "nori.example" },
    });
    assert.equal((await signOut(request())).status, 403);
    assert.equal((await signIn(request())).status, 403);
  });
});
