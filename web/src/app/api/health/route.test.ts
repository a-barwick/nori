import { test } from "node:test";
import assert from "node:assert/strict";
import { asFetch } from "@/lib/test-fetch";
import { GET } from "./route";

test("Mac readiness requires a valid identity map and reachable IAP policy", async () => {
  const originalSource = process.env.NORI_OIDC_IAP_POLICY_RESOURCE;
  const originalIdentities = process.env.NORI_OIDC_IAP_ALLOWED_IDENTITIES;
  const originalFetch = globalThis.fetch;
  const resource = `projects/${Date.now()}/iap_web/cloud_run-us-central1/services/nori-web`;
  const unmatchedResource = `projects/${Date.now() + 1}/iap_web/cloud_run-us-central1/services/nori-web`;
  try {
    delete process.env.NORI_OIDC_IAP_POLICY_RESOURCE;
    delete process.env.NORI_OIDC_IAP_ALLOWED_IDENTITIES;
    assert.equal((await GET()).status, 200);

    process.env.NORI_OIDC_IAP_POLICY_RESOURCE = resource;
    assert.equal((await GET()).status, 503);
    process.env.NORI_OIDC_IAP_ALLOWED_IDENTITIES = '{"subject":"USER@example.test"}';
    assert.equal((await GET()).status, 503);

    process.env.NORI_OIDC_IAP_ALLOWED_IDENTITIES = '{"subject":"user@example.test"}';
    globalThis.fetch = asFetch(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.startsWith("http://metadata.google.internal/")) return Response.json({ access_token: "test-policy-token" });
      if (url === `https://iap.googleapis.com/v1/${resource}:getIamPolicy`) {
        return Response.json({ bindings: [{ role: "roles/iap.httpsResourceAccessor", members: ["user:user@example.test"] }] });
      }
      if (url === `https://iap.googleapis.com/v1/${unmatchedResource}:getIamPolicy`) {
        return Response.json({ bindings: [{ role: "roles/iap.httpsResourceAccessor", members: ["user:other@example.test"] }] });
      }
      throw new Error(`Unexpected fetch URL: ${url}`);
    });
    assert.equal((await GET()).status, 200);

    process.env.NORI_OIDC_IAP_POLICY_RESOURCE = unmatchedResource;
    assert.equal((await GET()).status, 503);

    delete process.env.NORI_OIDC_IAP_POLICY_RESOURCE;
    assert.equal((await GET()).status, 503);
  } finally {
    globalThis.fetch = originalFetch;
    if (originalSource === undefined) delete process.env.NORI_OIDC_IAP_POLICY_RESOURCE;
    else process.env.NORI_OIDC_IAP_POLICY_RESOURCE = originalSource;
    if (originalIdentities === undefined) delete process.env.NORI_OIDC_IAP_ALLOWED_IDENTITIES;
    else process.env.NORI_OIDC_IAP_ALLOWED_IDENTITIES = originalIdentities;
  }
});
