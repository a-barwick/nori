import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import type { Capabilities } from "@/lib/contracts/assistant";
import type { ModelSelection } from "@/lib/types";
import { normalizeModelSelection } from "./modelSelection";

const fixture: {
  catalog: Capabilities["model_profiles"];
  cases: { name: string; catalogAvailable: boolean; selection: ModelSelection; expected: ModelSelection }[];
} = JSON.parse(readFileSync(new URL("../../../../contracts/assistant/client/model-selections.json", import.meta.url), "utf8"));

for (const item of fixture.cases) {
  test(item.name, () => {
    assert.deepEqual(normalizeModelSelection(item.selection, item.catalogAvailable ? fixture.catalog : undefined), item.expected);
  });
}
