#!/usr/bin/env node
// Consolidate only this project's known env files. Never print values.
import { chmodSync, closeSync, fsyncSync, lstatSync, openSync, readFileSync, renameSync, symlinkSync, writeFileSync, unlinkSync } from "node:fs";
import { resolve, dirname, relative } from "node:path";
import { parseEnv } from "node:util";
import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";

const root = resolve(process.argv[2] || ".");
const destination = resolve(root, ".env");
const paths = [destination, resolve(root, "core/.env"), resolve(root, "web/.env")];
const files = paths.flatMap(path => {
  try {
    if (lstatSync(path).isSymbolicLink()) return [];
    return [{ path, raw: readFileSync(path, "utf8") }];
  } catch (error) {
    if (error.code === "ENOENT") return [];
    throw error;
  }
});
const merged = {};
try {
  if (lstatSync(destination).isSymbolicLink()) throw new Error("Root .env must be the real file.");
  if (spawnSync("git", ["check-ignore", "--quiet", ".env"], { cwd: root }).status !== 0) throw new Error("Root .env must be gitignored.");
  for (const file of files) {
    const keys = new Set();
    for (const line of file.raw.split("\n")) {
      const key = line.match(/^\s*(?:export\s+)?([A-Za-z_][A-Za-z_0-9]*)\s*=/)?.[1];
      if (key && keys.has(key)) throw new Error(`Duplicate key ${key} in ${relative(root, file.path)}; source left unchanged.`);
      if (key) keys.add(key);
    }
    for (const [key, value] of Object.entries(parseEnv(file.raw))) {
      if (key in merged && merged[key] !== value) throw new Error(`Conflicting ${key}; all files left unchanged.`);
      merged[key] = value;
    }
  }
  // Single quotes preserve dollar signs for dotenv readers. Refuse unsupported
  // values before writing rather than changing their meaning across loaders.
  const lines = Object.entries(merged).sort(([a], [b]) => a.localeCompare(b)).map(([key, value]) => {
    if (/[\r\n']/.test(value)) throw new Error(`Cannot safely serialize ${key}; all files left unchanged.`);
    return `${key}='${value}'`;
  });
  const output = "# One project environment. core/.env and web/.env link here.\n" + lines.join("\n") + "\n";
  const parsed = parseEnv(output);
  if (Object.keys(parsed).length !== Object.keys(merged).length || Object.entries(merged).some(([k,v]) => parsed[k] !== v)) throw new Error("Round-trip validation failed; files left unchanged.");
  for (const file of files) if (readFileSync(file.path,"utf8") !== file.raw) throw new Error("An env file changed during consolidation; retry after its writer finishes.");
  const temporary = `${destination}.${randomUUID()}.tmp`;
  try {
    writeFileSync(temporary, output, { flag: "wx", mode: 0o600, flush: true });
    renameSync(temporary, destination);
    const directory = openSync(root, "r");
    try { fsyncSync(directory); } finally { closeSync(directory); }
  } finally { try { unlinkSync(temporary); } catch {} }
  chmodSync(destination, 0o600);
  const saved = parseEnv(readFileSync(destination, "utf8"));
  for (const file of files) for (const [key,value] of Object.entries(parseEnv(file.raw))) if (saved[key] !== value) throw new Error("Saved environment verification failed; original secondary files retained.");
  for (const path of paths.slice(1)) {
    const source = files.find(file => file.path === path);
    if (source && readFileSync(path, "utf8") !== source.raw) throw new Error("Secondary env file changed; its original is retained.");
    const link = `${path}.${randomUUID()}.tmp`;
    try { symlinkSync(relative(dirname(path), destination), link); renameSync(link, path); }
    finally { try { unlinkSync(link); } catch {} }
  }
  console.log(JSON.stringify({ file: destination, keys: Object.keys(saved).length, mode: "600", legacyPaths: "symlinks", verified: true }));
} catch (error) {
  console.error(error.code ? "Environment consolidation failed; no values printed." : error.message);
  process.exitCode = 1;
}
