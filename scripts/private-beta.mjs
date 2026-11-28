import { sign } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";

const origin = "https://api.appstoreconnect.apple.com";
const group = "/v1/betaGroups/67c1a0f8-4939-4652-93b2-e1c4b154e020";
const appID = "6839451136";

function apiToken({ ASC_KEY_ID: kid, ASC_ISSUER_ID: iss, ASC_PRIVATE_KEY: key }) {
  if (!kid || !iss || !key) throw new Error("App Store Connect credentials are missing.");
  const encode = value => Buffer.from(JSON.stringify(value)).toString("base64url");
  const iat = Math.floor(Date.now() / 1000);
  const payload = [
    encode({ alg: "ES256", kid, typ: "JWT" }),
    encode({ iss, iat, exp: iat + 600, aud: "appstoreconnect-v1" }),
  ].join(".");
  return payload + "." + sign("sha256", Buffer.from(payload), { key, dsaEncoding: "ieee-p1363" }).toString("base64url");
}

export async function privateTesters(get) {
  const checkGroup = async () => {
    const { data } = await get(new URL(group + "?include=app&fields[betaGroups]=app,isInternalGroup,publicLinkEnabled", origin));
    if (data?.type !== "betaGroups" || group !== "/v1/betaGroups/" + data.id ||
        data.relationships?.app?.data?.id !== appID || data.attributes?.isInternalGroup !== false ||
        data.attributes?.publicLinkEnabled !== false) {
      throw new Error("The configured Nori group must be external and email-only.");
    }
  };

  await checkGroup();
  const emails = new Set();
  const pages = new Set();
  const path = group + "/betaTesters";
  let next = new URL(path + "?limit=200&fields[betaTesters]=email,inviteType,state", origin);
  let seen = 0;
  let total;
  while (next) {
    if (next.origin !== origin || next.pathname !== path || next.username || next.password || pages.has(next.href)) {
      throw new Error("Invalid App Store Connect pagination.");
    }
    pages.add(next.href);
    const page = await get(next);
    if (!Array.isArray(page.data) || !page.links || !Number.isSafeInteger(page.meta?.paging?.total) ||
        (total !== undefined && total !== page.meta.paging.total)) {
      throw new Error("Incomplete App Store Connect roster. Retry the sync.");
    }
    total = page.meta.paging.total;
    for (const tester of page.data) {
      const { email, inviteType, state } = tester.attributes ?? {};
      if (tester.type !== "betaTesters" || inviteType !== "EMAIL") {
        throw new Error("The private group contains a tester without a named email invitation.");
      }
      seen++;
      if (state === "REVOKED") continue;
      if (!["NOT_INVITED", "INVITED", "ACCEPTED", "INSTALLED"].includes(state) ||
          typeof email !== "string" || !/^[a-z0-9+_.-]+@[a-z0-9.-]+\.[a-z0-9-]+$/i.test(email)) {
        throw new Error("Apple returned an unknown tester state or an unsupported email address.");
      }
      const normalized = email.toLowerCase();
      if (emails.has(normalized)) throw new Error("Duplicate tester in App Store Connect. Retry the sync.");
      emails.add(normalized);
    }
    next = page.links.next ? new URL(page.links.next, origin) : null;
  }
  if (seen !== total) throw new Error("App Store Connect returned a partial tester roster.");
  await checkGroup();
  return [...emails].sort();
}

async function main() {
  const output = process.argv[2];
  if (!output) throw new Error("Supply a private destination file for Terraform variables.");
  const token = apiToken(process.env);
  const emails = await privateTesters(async url => {
    const response = await fetch(url, {
      headers: { Authorization: "Bearer " + token },
      redirect: "error", signal: AbortSignal.timeout(15000),
    });
    if (!response.ok) throw new Error("App Store Connect returned HTTP " + response.status);
    return response.json();
  });
  await writeFile(output, JSON.stringify({ tester_emails: emails }), { mode: 0o600, flag: "wx" });
  console.log("Read " + emails.length + " private tester(s) from App Store Connect.");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
