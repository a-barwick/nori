#!/usr/bin/env node
// Generates web/src/lib/contracts/assistant.ts from contracts/assistant/schemas.
// Handles the JSON Schema constructs those files use: $ref (local and cross-file),
// type, properties, required, enum, const, items, oneOf, anyOf, allOf (merged), and
// allOf of if/then on a const discriminator (emitted as a union). Validation-only
// keywords (pattern, format, not, minimum, ...) do not change the TypeScript shape.
// Run: bun run contracts   (or: node scripts/contract-types.mjs [--check])
import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const schemaDir = join(root, "contracts", "assistant", "schemas");
const outFile = join(root, "web", "src", "lib", "contracts", "assistant.ts");

const files = readdirSync(schemaDir).filter((name) => name.endsWith(".schema.json")).sort();
const schemas = Object.fromEntries(files.map((name) => [name, JSON.parse(readFileSync(join(schemaDir, name), "utf8"))]));

function resolvePointer(file, pointer) {
  let node = schemas[file];
  for (const key of pointer.replace(/^\//, "").split("/")) node = node?.[key.replace(/~1/g, "/").replace(/~0/g, "~")];
  if (node === undefined) throw new Error(`Unresolved $ref ${file}#${pointer}`);
  return node;
}

function refTarget(ref, file) {
  const [target, pointer = ""] = ref.split("#");
  return { file: target || file, pointer };
}

const key = (name) => (/^[A-Za-z_$][\w$]*$/.test(name) ? name : JSON.stringify(name));
const doc = (text, pad) => (text ? `${pad}/** ${text.replace(/\*\//g, "*\\/")} */\n` : "");

// Merge allOf members into one object schema (later members refine earlier ones).
function mergeAllOf(schema, file) {
  const parts = schema.allOf.map((part) => (part.$ref ? { ...resolveSchema(part, file), file: refTarget(part.$ref, file).file } : { schema: part, file }));
  const merged = { type: "object", properties: {}, required: [] };
  const conditionals = [];
  for (const { schema: part, file: partFile } of parts.map((p) => (p.schema ? p : { schema: p, file }))) {
    if (part.if && part.then) {
      // Only a const discriminator changes the shape; other conditionals are validation rules.
      const entry = Object.entries(part.if.properties ?? {})[0];
      if (entry && entry[1].const !== undefined) conditionals.push({ part, file: partFile });
      continue;
    }
    if (part.allOf) {
      const inner = mergeAllOf(part, partFile);
      Object.assign(merged.properties, inner.schema.properties);
      merged.required.push(...inner.schema.required);
      continue;
    }
    for (const [name, value] of Object.entries(part.properties ?? {})) {
      // A refinement that only adds constraints (not, nested properties without a type) keeps the earlier shape.
      const refinement = !value.$ref && !value.type && !value.enum && value.const === undefined && !value.oneOf && !value.anyOf && !value.allOf && !value.items;
      if (refinement && merged.properties[name]) continue;
      merged.properties[name] = { schema: value, file: partFile };
    }
    merged.required.push(...(part.required ?? []));
    if (part.additionalProperties === false) merged.additionalProperties = false;
    if (part.description && !merged.description) merged.description = part.description;
  }
  return { schema: merged, conditionals };
}

function resolveSchema(schema, file) {
  const { file: target, pointer } = refTarget(schema.$ref, file);
  return { schema: resolvePointer(target, pointer), file: target };
}

function typeOf(schema, file, pad) {
  if (schema === true || schema === undefined || Object.keys(schema).length === 0) return "unknown";
  if (schema.$ref) {
    const { file: target, pointer } = refTarget(schema.$ref, file);
    const match = pointer.match(/^\/\$defs\/([A-Za-z0-9_]+)$/);
    if (match) return match[1];
    return typeOf(resolvePointer(target, pointer), target, pad);
  }
  if (schema.const !== undefined) return JSON.stringify(schema.const);
  if (schema.enum) return schema.enum.map((value) => JSON.stringify(value)).join(" | ");
  if (schema.oneOf || schema.anyOf) {
    // Members that only list required fields are validation rules, not shapes.
    const members = (schema.oneOf ?? schema.anyOf).filter((member) => !(member.required && Object.keys(member).every((name) => name === "required" || name === "description"))).map((member) => typeOf(member, file, pad));
    if (!members.length) return objectType({ ...schema, oneOf: undefined, anyOf: undefined }, file, pad);
    const base = schema.properties || schema.type === "object" ? objectType({ ...schema, oneOf: undefined, anyOf: undefined }, file, pad) : "";
    const union = members.length > 1 ? `(${members.join(" | ")})` : members[0];
    return base ? `${base} & ${union}` : union;
  }
  if (schema.allOf) {
    const { allOf, ...own } = schema;
    const { schema: merged, conditionals } = mergeAllOf({ allOf: own.properties ? [own, ...allOf] : allOf }, file);
    if (!conditionals.length) return objectType(merged, file, pad, true);
    const discriminators = new Set(conditionals.map(({ part }) => Object.keys(part.if.properties)[0]));
    const base = objectType({ ...merged, properties: Object.fromEntries(Object.entries(merged.properties).filter(([name]) => !discriminators.has(name))) }, file, pad, true);
    const variants = conditionals.map(({ part, file: partFile }) => {
      const [discriminator, condition] = Object.entries(part.if.properties)[0];
      const overrides = { type: "object", properties: { [discriminator]: { const: condition.const }, ...(part.then.properties ?? {}) }, required: [discriminator, ...(part.then.required ?? []), ...Object.keys(part.then.properties ?? {}).filter((name) => merged.required.includes(name))] };
      return objectType(overrides, partFile, pad);
    });
    for (const name of discriminators) if (!merged.required.includes(name)) variants.push(`{\n${pad}  ${key(name)}?: undefined;\n${pad}}`);
    return `${base} & (${variants.join(" | ")})`;
  }
  if (Array.isArray(schema.type)) return schema.type.map((type) => typeOf({ ...schema, type }, file, pad)).join(" | ");
  switch (schema.type) {
    case "string": return "string";
    case "integer":
    case "number": return "number";
    case "boolean": return "boolean";
    case "null": return "null";
    case "array": return `${wrap(typeOf(schema.items, file, pad))}[]`;
    case "object": return objectType(schema, file, pad);
    default: return schema.properties ? objectType(schema, file, pad) : "unknown";
  }
}

const wrap = (type) => (/[|&]/.test(type) && !/^\(.*\)$/.test(type) ? `(${type})` : type);

function objectType(schema, file, pad, preResolved = false) {
  const properties = Object.entries(schema.properties ?? {});
  const additional = schema.additionalProperties;
  if (!properties.length) {
    if (additional && typeof additional === "object") return `Record<string, ${typeOf(additional, file, pad)}>`;
    return "Record<string, unknown>";
  }
  const required = new Set(schema.required ?? []);
  const inner = pad + "  ";
  const lines = properties.map(([name, value]) => {
    const { schema: prop, file: propFile } = preResolved && value.schema !== undefined && value.file ? value : { schema: value, file };
    return `${doc(prop.description, inner)}${inner}${key(name)}${required.has(name) ? "" : "?"}: ${typeOf(prop, propFile, inner)};`;
  });
  return `{\n${lines.join("\n")}\n${pad}}`;
}

let out = `// Generated by web/scripts/contract-types.mjs from contracts/assistant/schemas. Do not edit.\n`;
out += `// Encoding: ${JSON.parse(readFileSync(join(root, "contracts", "assistant", "fixtures", "index.json"), "utf8")).revision}\n`;
for (const file of files) {
  out += `\n// ${file}\n`;
  for (const [name, definition] of Object.entries(schemas[file].$defs)) {
    out += `${doc(definition.description, "")}export type ${name} = ${typeOf(definition, file, "")};\n`;
  }
}

if (process.argv.includes("--check")) {
  const current = readFileSync(outFile, "utf8");
  if (current !== out) { console.error("web/src/lib/contracts/assistant.ts is stale. Run: bun run contracts"); process.exit(1); }
  console.log("contracts types are current");
} else {
  writeFileSync(outFile, out);
  console.log(`wrote ${outFile}`);
}
