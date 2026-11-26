const policyLifetimeMs = 30_000;
const failureLifetimeMs = 5_000;
const policyPath = /^projects\/\d+\/iap_web\/cloud_run-[a-z0-9-]+\/services\/[a-z0-9-]+$/;
const namedUser = /^user:([a-z0-9+_.-]+@[a-z0-9.-]+)$/i;

type Snapshot = { resource: string; expiresAt: number; allowed: Set<string> | null };
let snapshot: Snapshot | undefined;
let pending: { resource: string; result: Promise<Set<string> | null> } | undefined;

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
