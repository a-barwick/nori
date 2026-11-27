#!/usr/bin/env node
import { readFileSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "..");
const defaultInput = resolve(repoRoot, "api/openapi.json");
const defaultOutput = resolve(repoRoot, "ios/NoriKit/Sources/NoriCore/Generated/OpenAPI.swift");

const SWIFT_KEYWORDS = new Set([
  "associatedtype", "class", "deinit", "enum", "extension", "fileprivate",
  "func", "import", "init", "inout", "internal", "let", "open", "operator",
  "private", "precedencegroup", "protocol", "public", "rethrows", "static",
  "struct", "subscript", "typealias", "var", "break", "case", "catch",
  "continue", "default", "defer", "do", "else", "fallthrough", "for", "guard",
  "if", "in", "repeat", "return", "throw", "switch", "where", "while", "as",
  "false", "is", "nil", "self", "Self", "super", "throws", "true", "try",
  "Type", "Protocol",
]);

function escapeSwiftIdent(name) {
  if (!name) return "_value";
  let cleaned = name.replace(/[^A-Za-z0-9_]/g, "_");
  if (/^[0-9]/.test(cleaned)) cleaned = "_" + cleaned;
  return SWIFT_KEYWORDS.has(cleaned) ? `\`${cleaned}\`` : cleaned;
}

function toCamelCase(raw) {
  if (!raw) return "value";
  const parts = raw
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .split(/[^A-Za-z0-9]+/)
    .filter(Boolean);
  if (parts.length === 0) return "value";
  const first = parts[0].toLowerCase();
  const rest = parts.slice(1).map((p) => {
    const low = p.toLowerCase();
    if (low === "id") return "ID";
    if (low === "ids") return "IDs";
    if (low === "url") return "URL";
    if (low === "urls") return "URLs";
    if (low === "usd") return "USD";
    if (low === "mcp") return "MCP";
    if (low === "oauth") return "OAuth";
    if (low === "sse") return "SSE";
    if (low === "api") return "API";
    if (low === "json") return "JSON";
    if (low === "html") return "HTML";
    if (low === "dst") return "DST";
    if (low === "ics") return "ICS";
    if (low === "rsvp") return "RSVP";
    if (low === "byok") return "BYOK";
    if (low === "rrule") return "RRule";
    return low.charAt(0).toUpperCase() + low.slice(1);
  });
  return first + rest.join("");
}

function toPascalCase(raw) {
  const camel = toCamelCase(raw);
  if (!camel) return "Value";
  if (camel === "id") return "ID";
  if (camel === "url") return "URL";
  if (camel === "mcp") return "MCP";
  if (camel === "api") return "API";
  if (camel === "oauth") return "OAuth";
  return camel.charAt(0).toUpperCase() + camel.slice(1);
}

function refName(ref) {
  const prefix = "#/components/schemas/";
  if (!ref || !ref.startsWith(prefix)) return null;
  return ref.slice(prefix.length);
}

function resolveSchema(schema, schemas, seen = new Set()) {
  if (!schema || typeof schema !== "object") return {};
  if (schema.$ref) {
    const name = refName(schema.$ref);
    if (name && schemas[name] && !seen.has(name)) {
      const nextSeen = new Set(seen);
      nextSeen.add(name);
      return resolveSchema(schemas[name], schemas, nextSeen);
    }
  }
  return schema;
}

function collectObjectProperties(schema, schemas) {
  const resolved = resolveSchema(schema, schemas);
  const properties = {};
  const required = new Set();

  if (Array.isArray(resolved.allOf)) {
    for (const sub of resolved.allOf) {
      const subCollected = collectObjectProperties(sub, schemas);
      for (const [k, v] of Object.entries(subCollected.properties)) {
        properties[k] = v;
      }
      for (const r of subCollected.required) {
        required.add(r);
      }
    }
  }

  if (resolved.properties && typeof resolved.properties === "object") {
    for (const [k, v] of Object.entries(resolved.properties)) {
      properties[k] = v;
    }
  }
  if (Array.isArray(resolved.required)) {
    for (const r of resolved.required) {
      required.add(r);
    }
  }
  return { properties, required };
}

function baseTypeOf(schema) {
  if (!schema || typeof schema !== "object") return { kind: null, nullable: false };
  let nullable = Boolean(schema.nullable);
  let t = schema.type;
  if (Array.isArray(t)) {
    nullable = nullable || t.includes("null");
    t = t.find((x) => x !== "null") || null;
  }
  return { kind: t || null, nullable };
}

function detectDiscriminator(variants, schemas) {
  const candidateFields = ["kind", "data_kind", "status", "action", "type"];
  for (const field of candidateFields) {
    let valid = true;
    const mapping = [];
    const allowNilFlags = [];
    let nilFallbackCount = 0;
    const seenValues = new Set();
    for (const variant of variants) {
      const { properties, required } = collectObjectProperties(variant, schemas);
      const prop = properties[field];
      const isRequired = required.has(field);
      if (!isRequired) {
        nilFallbackCount++;
        if (nilFallbackCount > 1) {
          valid = false;
          break;
        }
      }
      if (!prop) {
        if (isRequired) {
          valid = false;
          break;
        }
        mapping.push([]);
        allowNilFlags.push(true);
        continue;
      }
      if (!Array.isArray(prop.enum) || prop.enum.length === 0) {
        valid = false;
        break;
      }
      const vals = prop.enum.filter((v) => typeof v === "string");
      if (vals.length !== prop.enum.length) {
        valid = false;
        break;
      }
      for (const v of vals) {
        if (seenValues.has(v)) {
          valid = false;
          break;
        }
        seenValues.add(v);
      }
      if (!valid) break;
      mapping.push(vals);
      allowNilFlags.push(!isRequired);
    }
    if (valid && mapping.length === variants.length) {
      return { field, variantValues: mapping, allowNilFlags };
    }
  }
  return null;
}

export function generateSwiftOpenAPI(spec) {
  const schemas = spec.components?.schemas || {};
  const paths = spec.paths || {};
  const lines = [];

  lines.push("// Code generated from api/openapi.yaml by scripts/gen-swift-openapi.mjs; DO NOT EDIT.");
  lines.push("import Foundation");
  lines.push("");
  lines.push("public enum OpenAPI {");

  // 1. Paths
  lines.push("    public enum Path {");
  lines.push("        private static func encodeSegment(_ raw: String) -> String {");
  lines.push("            var allowed = CharacterSet.urlPathAllowed");
  lines.push("            allowed.remove(charactersIn: \"/\")");
  lines.push("            return raw.addingPercentEncoding(withAllowedCharacters: allowed) ?? raw");
  lines.push("        }");
  lines.push("");

  const usedPathNames = new Set();
  for (const rawPath of Object.keys(paths).sort()) {
    const segments = rawPath
      .replace(/^\/v1\//, "")
      .split("/")
      .filter(Boolean);
    const params = [];
    const nameParts = [];
    for (const seg of segments) {
      const match = /^\{([^}]+)\}$/.exec(seg);
      if (match) {
        params.push(match[1]);
      } else {
        nameParts.push(seg);
      }
    }
    let ident = toCamelCase(nameParts.join("_") || "root");
    if (params.length > 0 && usedPathNames.has(ident)) {
      ident = toCamelCase([...nameParts, "by", ...params].join("_"));
    }
    let uniqueIdent = ident;
    let suffix = 2;
    while (usedPathNames.has(uniqueIdent)) {
      uniqueIdent = `${ident}${suffix++}`;
    }
    usedPathNames.add(uniqueIdent);
    const swiftIdent = escapeSwiftIdent(uniqueIdent);

    if (params.length === 0) {
      lines.push(`        public static let ${swiftIdent} = "${rawPath}"`);
    } else {
      const paramDecls = params.map((p) => {
        const argName = escapeSwiftIdent(toCamelCase(p));
        return `${argName}: String`;
      });
      const interpolated = rawPath.replace(/\{([^}]+)\}/g, (_, p) => {
        const argName = escapeSwiftIdent(toCamelCase(p));
        return `\\(encodeSegment(${argName}))`;
      });
      lines.push(`        public static func ${swiftIdent}(${paramDecls.join(", ")}) -> String {`);
      lines.push(`            "${interpolated}"`);
      lines.push("        }");
    }
  }
  lines.push("    }");
  lines.push("");

  // 2. Schemas
  function emitEnum(typeName, enumValues, indent) {
    const out = [];
    out.push(`${indent}public enum ${typeName}: String, Sendable, Codable, Hashable, CaseIterable {`);
    const usedCases = new Set();
    for (const val of enumValues) {
      let caseName = val === "" ? "empty" : toCamelCase(String(val));
      if (usedCases.has(caseName)) {
        let i = 2;
        while (usedCases.has(`${caseName}${i}`)) i++;
        caseName = `${caseName}${i}`;
      }
      usedCases.add(caseName);
      out.push(`${indent}    case ${escapeSwiftIdent(caseName)} = ${JSON.stringify(String(val))}`);
    }
    out.push(`${indent}}`);
    return out;
  }

  function resolveSwiftType(propSchema, nestedTypeName, nestedDecls, indent) {
    if (!propSchema || typeof propSchema !== "object" || Object.keys(propSchema).length === 0) {
      return { type: "AnyJSON", nullable: false };
    }
    if (propSchema.$ref) {
      const target = refName(propSchema.$ref);
      if (target) {
        return { type: `OpenAPI.${escapeSwiftIdent(target)}`, nullable: false };
      }
    }
    if (Array.isArray(propSchema.oneOf)) {
      nestedDecls.push(...emitOneOf(nestedTypeName, propSchema.oneOf, indent));
      return { type: nestedTypeName, nullable: false };
    }
    if (Array.isArray(propSchema.allOf)) {
      const { properties, required } = collectObjectProperties(propSchema, schemas);
      nestedDecls.push(...emitStruct(nestedTypeName, properties, required, indent));
      return { type: nestedTypeName, nullable: false };
    }

    const { kind, nullable } = baseTypeOf(propSchema);

    if (kind === "array" || propSchema.items) {
      const itemTypeName = nestedTypeName.endsWith("s")
        ? `${nestedTypeName.slice(0, -1)}Item`
        : `${nestedTypeName}Item`;
      const inner = resolveSwiftType(propSchema.items || {}, itemTypeName, nestedDecls, indent);
      const innerType = inner.nullable ? `${inner.type}?` : inner.type;
      return { type: `[${innerType}]`, nullable };
    }

    if (kind === "object" || propSchema.properties || propSchema.additionalProperties) {
      if (propSchema.properties && Object.keys(propSchema.properties).length > 0) {
        const req = new Set(Array.isArray(propSchema.required) ? propSchema.required : []);
        nestedDecls.push(...emitStruct(nestedTypeName, propSchema.properties, req, indent));
        return { type: nestedTypeName, nullable };
      }
      if (propSchema.additionalProperties && typeof propSchema.additionalProperties === "object") {
        const val = resolveSwiftType(propSchema.additionalProperties, `${nestedTypeName}Value`, nestedDecls, indent);
        const valType = val.nullable ? `${val.type}?` : val.type;
        return { type: `[String: ${valType}]`, nullable };
      }
      return { type: "[String: AnyJSON]", nullable };
    }

    if (kind === "string") return { type: "String", nullable };
    if (kind === "integer") return { type: "Int", nullable };
    if (kind === "number") return { type: "Double", nullable };
    if (kind === "boolean") return { type: "Bool", nullable };

    return { type: "AnyJSON", nullable };
  }

  function emitStruct(typeName, properties, requiredSet, indent) {
    const out = [];
    const nestedDecls = [];
    const propKeys = Object.keys(properties);
    const fields = [];
    const usedPropNames = new Set();

    for (const wireKey of propKeys) {
      const propSchema = properties[wireKey];
      let swiftProp = wireKey === "self" ? "isSelf" : toCamelCase(wireKey);
      if (usedPropNames.has(swiftProp)) {
        let i = 2;
        while (usedPropNames.has(`${swiftProp}${i}`)) i++;
        swiftProp = `${swiftProp}${i}`;
      }
      usedPropNames.add(swiftProp);

      const nestedName = escapeSwiftIdent(toPascalCase(wireKey));
      const { type, nullable } = resolveSwiftType(propSchema, nestedName, nestedDecls, indent + "    ");
      const isOptional = !requiredSet.has(wireKey) || nullable;
      fields.push({
        wireKey,
        swiftProp: escapeSwiftIdent(swiftProp),
        rawSwiftProp: swiftProp,
        type: isOptional ? `${type}?` : type,
        isOptional,
      });
    }

    out.push(`${indent}public struct ${typeName}: Sendable, Codable, Hashable {`);
    if (nestedDecls.length > 0) {
      out.push(...nestedDecls);
      out.push("");
    }
    for (const f of fields) {
      out.push(`${indent}    public var ${f.swiftProp}: ${f.type}`);
    }

    if (fields.length > 0) {
      out.push("");
      out.push(`${indent}    public init(`);
      fields.forEach((f, idx) => {
        const comma = idx < fields.length - 1 ? "," : "";
        const defaultVal = f.isOptional ? " = nil" : "";
        out.push(`${indent}        ${f.swiftProp}: ${f.type}${defaultVal}${comma}`);
      });
      out.push(`${indent}    ) {`);
      for (const f of fields) {
        out.push(`${indent}        self.${f.swiftProp} = ${f.swiftProp}`);
      }
      out.push(`${indent}    }`);

      const needsCodingKeys = fields.some((f) => f.rawSwiftProp !== f.wireKey);
      if (needsCodingKeys) {
        out.push("");
        out.push(`${indent}    enum CodingKeys: String, CodingKey {`);
        for (const f of fields) {
          if (f.rawSwiftProp === f.wireKey) {
            out.push(`${indent}        case ${f.swiftProp}`);
          } else {
            out.push(`${indent}        case ${f.swiftProp} = ${JSON.stringify(f.wireKey)}`);
          }
        }
        out.push(`${indent}    }`);
      }
    } else {
      out.push(`${indent}    public init() {}`);
    }

    out.push(`${indent}}`);
    return out;
  }

  function emitOneOf(typeName, variants, indent) {
    const out = [];
    const disc = detectDiscriminator(variants, schemas);
    const cases = [];
    const nestedDecls = [];
    const usedCaseNames = new Set();

    variants.forEach((variant, idx) => {
      let baseName;
      if (disc && disc.variantValues[idx].length > 0) {
        baseName = disc.variantValues[idx][0];
      } else if (disc && disc.allowNilFlags[idx]) {
        baseName = "plain";
      } else {
        baseName = `variant${idx + 1}`;
      }
      let caseName = toCamelCase(baseName);
      if (usedCaseNames.has(caseName)) {
        let i = 2;
        while (usedCaseNames.has(`${caseName}${i}`)) i++;
        caseName = `${caseName}${i}`;
      }
      usedCaseNames.add(caseName);
      const payloadName = `${toPascalCase(caseName)}Payload`;

      let payloadType;
      if (variant.$ref) {
        payloadType = `OpenAPI.${escapeSwiftIdent(refName(variant.$ref))}`;
      } else {
        const { properties, required } = collectObjectProperties(variant, schemas);
        nestedDecls.push(...emitStruct(payloadName, properties, required, indent + "    "));
        payloadType = payloadName;
      }

      cases.push({
        caseName: escapeSwiftIdent(caseName),
        payloadType,
        discValues: disc ? disc.variantValues[idx] : null,
        allowNil: disc ? disc.allowNilFlags[idx] : false,
      });
    });

    out.push(`${indent}public enum ${typeName}: Sendable, Codable, Hashable {`);
    if (nestedDecls.length > 0) {
      out.push(...nestedDecls);
      out.push("");
    }
    for (const c of cases) {
      out.push(`${indent}    case ${c.caseName}(${c.payloadType})`);
    }
    out.push("");

    if (disc) {
      const anyOptional = cases.some((c) => c.allowNil);
      out.push(`${indent}    private enum DiscriminatorKey: String, CodingKey {`);
      out.push(`${indent}        case discriminator = ${JSON.stringify(disc.field)}`);
      out.push(`${indent}    }`);
      out.push("");
      out.push(`${indent}    public init(from decoder: any Decoder) throws {`);
      out.push(`${indent}        let container = try decoder.container(keyedBy: DiscriminatorKey.self)`);
      if (anyOptional) {
        out.push(`${indent}        let kind = try container.decodeIfPresent(String.self, forKey: .discriminator)`);
      } else {
        out.push(`${indent}        let kind = try container.decode(String.self, forKey: .discriminator)`);
      }
      out.push(`${indent}        switch kind {`);
      for (const c of cases) {
        const parts = c.discValues.map((v) => JSON.stringify(v));
        if (c.allowNil) parts.push("nil");
        out.push(`${indent}        case ${parts.join(", ")}:`);
        out.push(`${indent}            self = .${c.caseName}(try ${c.payloadType}(from: decoder))`);
      }
      out.push(`${indent}        default:`);
      out.push(`${indent}            throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported ${typeName} ${disc.field}: \\(String(describing: kind))")`);
      out.push(`${indent}        }`);
      out.push(`${indent}    }`);
    } else {
      out.push(`${indent}    public init(from decoder: any Decoder) throws {`);
      for (const c of cases) {
        out.push(`${indent}        if let value = try? ${c.payloadType}(from: decoder) {`);
        out.push(`${indent}            self = .${c.caseName}(value)`);
        out.push(`${indent}            return`);
        out.push(`${indent}        }`);
      }
      out.push(`${indent}        throw DecodingError.dataCorrupted(DecodingError.Context(codingPath: decoder.codingPath, debugDescription: "Could not decode ${typeName}"))`);
      out.push(`${indent}    }`);
    }

    out.push("");
    out.push(`${indent}    public func encode(to encoder: any Encoder) throws {`);
    out.push(`${indent}        switch self {`);
    for (const c of cases) {
      out.push(`${indent}        case .${c.caseName}(let value):`);
      out.push(`${indent}            try value.encode(to: encoder)`);
    }
    out.push(`${indent}        }`);
    out.push(`${indent}    }`);
    out.push(`${indent}}`);
    return out;
  }

  for (const schemaName of Object.keys(schemas).sort()) {
    const schema = schemas[schemaName];
    const swiftName = escapeSwiftIdent(schemaName);

    if (Array.isArray(schema.oneOf)) {
      lines.push(...emitOneOf(swiftName, schema.oneOf, "    "));
      lines.push("");
      continue;
    }

    if (Array.isArray(schema.allOf)) {
      const { properties, required } = collectObjectProperties(schema, schemas);
      lines.push(...emitStruct(swiftName, properties, required, "    "));
      lines.push("");
      continue;
    }

    if (Array.isArray(schema.enum) && schema.enum.every((v) => typeof v === "string")) {
      lines.push(...emitEnum(swiftName, schema.enum, "    "));
      lines.push("");
      continue;
    }

    const { kind } = baseTypeOf(schema);
    if (kind === "object" || schema.properties) {
      const req = new Set(Array.isArray(schema.required) ? schema.required : []);
      lines.push(...emitStruct(swiftName, schema.properties || {}, req, "    "));
      lines.push("");
      continue;
    }

    if (kind === "array" && schema.items) {
      const nested = [];
      const inner = resolveSwiftType(schema.items, `${schemaName}Item`, nested, "    ");
      lines.push(...nested);
      const innerType = inner.nullable ? `${inner.type}?` : inner.type;
      lines.push(`    public typealias ${swiftName} = [${innerType}]`);
      lines.push("");
      continue;
    }

    if (kind === "string") {
      lines.push(`    public typealias ${swiftName} = String`);
      lines.push("");
      continue;
    }
    if (kind === "integer") {
      lines.push(`    public typealias ${swiftName} = Int`);
      lines.push("");
      continue;
    }
    if (kind === "number") {
      lines.push(`    public typealias ${swiftName} = Double`);
      lines.push("");
      continue;
    }
    if (kind === "boolean") {
      lines.push(`    public typealias ${swiftName} = Bool`);
      lines.push("");
      continue;
    }

    lines.push(`    public typealias ${swiftName} = AnyJSON`);
    lines.push("");
  }

  lines.push("}");
  lines.push("");
  return lines.join("\n");
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const checkOnly = process.argv.includes("--check");
  const spec = JSON.parse(readFileSync(defaultInput, "utf8"));
  const generated = generateSwiftOpenAPI(spec);

  if (checkOnly) {
    if (!existsSync(defaultOutput)) {
      console.error("ios/NoriKit/Sources/NoriCore/Generated/OpenAPI.swift is missing. Run: make api-types");
      process.exit(1);
    }
    const current = readFileSync(defaultOutput, "utf8");
    if (current !== generated) {
      console.error("ios/NoriKit/Sources/NoriCore/Generated/OpenAPI.swift is stale. Run: make api-types");
      process.exit(1);
    }
    process.exit(0);
  }

  mkdirSync(dirname(defaultOutput), { recursive: true });
  writeFileSync(defaultOutput, generated, "utf8");
}
