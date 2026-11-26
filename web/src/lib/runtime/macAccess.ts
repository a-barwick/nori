const policyLifetimeMs = 30_000;
const failureLifetimeMs = 5_000;
const policyPath = /^projects\/\d+\/iap_web\/cloud_run-[a-z0-9-]+\/services\/[a-z0-9-]+$/;
const namedUser = /^user:([a-z0-9+_.-]+@[a-z0-9.-]+)$/i;
const subjectID = /^[\x21-\x7e]{1,255}$/;
const normalizedEmail = /^[a-z0-9+_.-]+@[a-z0-9.-]+$/;

type Snapshot = { resource: string; expiresAt: number; allowed: Set<string> | null };
let snapshot: Snapshot | undefined;
let pending: { resource: string; result: Promise<Set<string> | null> } | undefined;

export function macAllowedIdentities(raw: string | undefined): Map<string, string> | null {
  if (!raw) return null;
  try {
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== "object" || Array.isArray(value)) return null;
    const entries = Object.entries(value);
    if (entries.length === 0) return null;
    const identities = new Map<string, string>();
    const emails = new Set<string>();
    for (const [subject, email] of entries) {
      if (!subjectID.test(subject) || typeof email !== "string" || !normalizedEmail.test(email) || emails.has(email)) return null;
      identities.set(subject, email);
      emails.add(email);
    }
    return identities;
  } catch {
    return null;
  }
}

export function directIAPUsers(policy: { bindings?: { role?: string; condition?: unknown; members?: string[] }[] }): Set<string> {
  const allowed = new Set<string>();
  for (const binding of policy.bindings ?? []) {
    if (binding.role !== "roles/iap.httpsResourceAccessor" || binding.condition) continue;
    for (const member of binding.members ?? []) {
      const email = namedUser.exec(member)?.[1];
      if (email) allowed.add(email.toLowerCase());
    }
  }
  return allowed;
}

async function readIAPPolicy(resource: string): Promise<Set<string>> {
  const metadata = await fetch("http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", {
    headers: { "Metadata-Flavor": "Google" },
    redirect: "error",
    signal: AbortSignal.timeout(3_000),
    cache: "no-store",
  });
  if (!metadata.ok) throw new Error("Cloud identity unavailable");
  const { access_token: token } = await metadata.json();
  if (typeof token !== "string" || !token) throw new Error("Cloud identity unavailable");

  const policy = await fetch(`https://iap.googleapis.com/v1/${resource}:getIamPolicy`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({ options: { requestedPolicyVersion: 3 } }),
    redirect: "error",
    signal: AbortSignal.timeout(3_000),
    cache: "no-store",
  });
  if (!policy.ok) throw new Error("IAP access policy unavailable");
  return directIAPUsers(await policy.json());
}

export function macAccessPolicy(resource: string): Promise<Set<string> | null> {
  if (!policyPath.test(resource)) return Promise.resolve(null);
  if (snapshot?.resource === resource && snapshot.expiresAt > Date.now()) return Promise.resolve(snapshot.allowed);
  if (pending?.resource === resource) return pending.result;

  const result = readIAPPolicy(resource).catch(() => null).then((allowed) => {
    const lifetime = allowed ? policyLifetimeMs : failureLifetimeMs;
    snapshot = { resource, allowed, expiresAt: Date.now() + lifetime };
    return allowed;
  }).finally(() => { if (pending?.resource === resource) pending = undefined; });
  pending = { resource, result };
  return result;
}
