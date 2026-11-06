// Real Next HTTP boundary checks. No credentials, production endpoints or paid calls.
import assert from 'node:assert/strict';
import { request as httpRequest } from 'node:http';
import { mkdir, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';

const target = new URL(process.env.NORI_PROXY_URL || 'http://127.0.0.1:3000');
if (target.protocol !== 'http:' || target.hostname !== '127.0.0.1' || target.pathname !== '/' || target.username || target.password || target.search || target.hash) throw new Error('Use a loopback Next origin');
const checks = [];
function call(path, { method = 'GET', headers = {}, body } = {}) {
  return new Promise((resolve, reject) => {
    const req = httpRequest({ hostname: target.hostname, port: target.port, path, method, headers: { Host: target.host, ...headers } }, res => {
      const chunks = [];
      res.on('data', chunk => chunks.push(chunk));
      res.on('end', () => resolve({ status: res.statusCode, text: Buffer.concat(chunks).toString() }));
    });
    req.on('error', reject);
    req.setTimeout(20000, () => req.destroy(new Error('Proxy timeout')));
    req.end(body);
  });
}
const readHeaders = { 'Sec-Fetch-Site': 'same-origin' };
const writeHeaders = { Origin: target.origin, 'Sec-Fetch-Site': 'same-origin', 'Content-Type': 'application/json' };
const capabilityResponse = await call('/api/v1/capabilities', { headers: readHeaders });
assert.equal(capabilityResponse.status, 200);
const capabilities = JSON.parse(capabilityResponse.text);
assert.equal(capabilities.isolated, true);
assert.equal(capabilities.fixture, false, 'Proxy checks must reach the real Go service');
assert.equal(capabilities.deterministic, true);
let passed = false;
try {
  for (const [name, headers] of [
    ['unsigned identity', { 'X-Goog-Authenticated-User-Email': 'accounts.google.com:austinbarwickdavis@gmail.com', Authorization: 'Bearer forged' }],
    ['malformed signed identity', { 'X-Goog-Iap-Jwt-Assertion': 'not.a.valid-jwt', 'X-Serverless-Authorization': 'Bearer forged' }],
  ]) {
    const result = await call('/api/v1/conversations', { headers: { ...readHeaders, Host: 'private.invalid', ...headers } });
    assert.equal(result.status, 403, name);
    checks.push(`${name}: refused at Next identity boundary`);
  }
  for (const [name, headers] of [
    ['no origin', { 'Content-Type': 'application/json' }],
    ['foreign origin', { ...writeHeaders, Origin: 'https://foreign.invalid' }],
    ['foreign fetch site', { ...writeHeaders, 'Sec-Fetch-Site': 'cross-site' }],
  ]) {
    assert.equal((await call('/api/v1/conversations', { method: 'POST', headers, body: '{}' })).status, 403, name);
    checks.push(`${name}: write refused`);
  }
  assert.equal((await call('/api/v1/conversations')).status, 403, 'reads require a same-origin browser');
  for (const body of [{ mode: 'assistant' }, { owner: 'someone-else' }, { identity: 'admin' }]) {
    assert.equal((await call('/api/v1/conversations', { method: 'POST', headers: writeHeaders, body: JSON.stringify(body) })).status, 400, 'server must reject requested identity or mode');
  }
  checks.push('browser-requested mode, owner and identity rejected by real service');
  const created = await call('/api/v1/conversations', { method: 'POST', headers: writeHeaders, body: '{}' });
  assert.equal(created.status, 201);
  const c = JSON.parse(created.text);
  assert.equal(c.mode, 'chat');
  assert.equal((await call(`/api/v1/conversations/${crypto.randomUUID()}`, { headers: readHeaders })).status, 404);
  for (const path of ['/api/v1/conversations/%2e%2e%2fcapabilities', `/api/v1/conversations/${c.id}/messages/extra`, '/api/v1/conversations/assistant%2f..%2fsecret']) {
    const status = (await call(path, { headers: readHeaders })).status;
    assert.ok(status === 400 || status === 404, `path refused: ${path} returned ${status}`);
  }
  checks.push('foreign conversation and traversal paths refused');
  assert.equal((await call(`/api/v1/conversations/${c.id}`, { headers: readHeaders })).status, 200);
  checks.push('authenticated same-origin create and read reach real service');
  passed = true;
} finally {
  const directory = resolve(import.meta.dirname, '../.artifacts/migration/proxy', new Date().toISOString().replaceAll(':', '-'));
  await mkdir(directory, { recursive: true });
  await writeFile(resolve(directory, 'report.json'), JSON.stringify({ passed, target: target.origin, capabilities, checks }, null, 2));
  console.log(`Proxy evidence: ${directory}`);
}
