import { test } from "bun:test";
import assert from "node:assert/strict";
import { directIAPUsers } from "./macAccess";

test("Mac Web mirrors explicit unconditional IAP users without widening their access", () => {
  assert.deepEqual([...directIAPUsers({
    bindings: [
      {
        role: "roles/iap.httpsResourceAccessor",
        members: ["user:Allowed@example.test", "group:team@example.test", "allAuthenticatedUsers"],
      },
      {
        role: "roles/iap.httpsResourceAccessor",
        condition: { expression: "false" },
        members: ["user:Conditional@example.test"],
      },
      {
        role: "roles/iap.admin",
        members: ["user:Admin@example.test"],
      },
    ],
  })], ["allowed@example.test"]);
  assert.deepEqual([...directIAPUsers({ bindings: [] })], []);
});
