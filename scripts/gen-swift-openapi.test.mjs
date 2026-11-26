import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { generateSwiftOpenAPI } from "./gen-swift-openapi.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "..");

test("generateSwiftOpenAPI produces deterministic Swift for all OpenAPI schemas and paths", () => {
  const spec = JSON.parse(readFileSync(resolve(repoRoot, "api/openapi.json"), "utf8"));
  const first = generateSwiftOpenAPI(spec);
  const second = generateSwiftOpenAPI(spec);
  assert.equal(first, second, "generator output must be deterministic");

  assert.match(first, /public enum OpenAPI \{/);
  assert.match(first, /public enum Path \{/);
  assert.match(first, /public static let capabilities = "\/v1\/capabilities"/);
  assert.match(first, /public static func workActions\(taskID: String\) -> String/);
  assert.match(first, /public struct Capabilities: Sendable, Codable, Hashable/);
  assert.match(first, /public struct ConversationView: Sendable, Codable, Hashable/);
  assert.match(first, /public enum Trigger: Sendable, Codable, Hashable/);
  assert.match(first, /public enum Interaction: Sendable, Codable, Hashable/);
  assert.match(first, /public enum Effect: String, Sendable, Codable, Hashable, CaseIterable/);
  assert.match(first, /case isSelf = "self"/);
});

test("generated OpenAPI.swift on disk matches api/openapi.json", () => {
  const spec = JSON.parse(readFileSync(resolve(repoRoot, "api/openapi.json"), "utf8"));
  const expected = generateSwiftOpenAPI(spec);
  const actual = readFileSync(
    resolve(repoRoot, "ios/NoriKit/Sources/NoriCore/Generated/OpenAPI.swift"),
    "utf8"
  );
  assert.equal(actual, expected, "OpenAPI.swift is out of sync; run `make api-types`");
});
