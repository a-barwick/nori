import { test } from "node:test";
import assert from "node:assert/strict";
import { directIAPUsers, macAllowedIdentities } from "./macAccess";

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

test("Mac subject and email configuration must be complete and one-to-one", () => {
  assert.deepEqual([...macAllowedIdentities('{"subject-1":"person@workspace.test","subject-2":"person@gmail.com"}')!], [
    ["subject-1", "person@workspace.test"], ["subject-2", "person@gmail.com"],
  ]);
  for (const raw of [
    undefined, "", "{", "{}", "[]", '{"123":"Person@example.com"}', '{"123":"not-an-email"}',
    '{"123":"person@example.com","456":"person@example.com"}', '{"123":"person@example.com","": "another@example.com"}',
    JSON.stringify({ ["x".repeat(256)]: "person@example.com" }),
  ]) {
    assert.equal(macAllowedIdentities(raw), null);
  }
});
