import assert from "node:assert/strict";
import { test } from "node:test";
import { privateTesters } from "./private-beta.mjs";

const groupID = "67c1a0f8-4939-4652-93b2-e1c4b154e020";
const path = "/v1/betaGroups/" + groupID + "/betaTesters";
const group = { data: {
  id: groupID, type: "betaGroups", attributes: { isInternalGroup: false, publicLinkEnabled: false },
  relationships: { app: { data: { type: "apps", id: "6839451136" } } },
} };
const tester = (email, state = "INVITED", inviteType = "EMAIL") => ({ type: "betaTesters", attributes: { email, state, inviteType } });
const page = (data, total = data.length, next = null) => ({ data, links: { next }, meta: { paging: { total } } });

function reader(...responses) {
  return async () => {
    const result = responses.shift();
    assert.ok(result, "unexpected request");
    return result;
  };
}

test("reads every page of the pinned private group before publishing the roster", async () => {
  const get = reader(group,
    page([tester("Beta@Gmail.com"), tester("gone@gmail.com", "REVOKED")], 3, path + "?cursor=2"),
    page([tester("a@example.com", "ACCEPTED")], 3), group);
  assert.deepEqual(await privateTesters(get), ["a@example.com", "beta@gmail.com"]);
  assert.deepEqual(await privateTesters(reader(group, page([]), group)), []);
});

test("rejects public groups, partial responses and non-email invitations", async () => {
  const publicGroup = structuredClone(group);
  publicGroup.data.attributes.publicLinkEnabled = true;
  for (const responses of [
    [publicGroup], [group, page([]), publicGroup], [group, { data: [] }],
    [group, page([], 3)], [group, page([], 1, "https://example.com" + path)],
    [group, page([tester("a@gmail.com", "ACCEPTED", "PUBLIC_LINK")])],
    [group, page([tester("allUsers")])],
  ]) {
    await assert.rejects(privateTesters(reader(...responses)));
  }
});
