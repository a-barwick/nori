import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { createHarness, until } from '../core/e2e/shared-service-harness';

// Run only through the main checkout's existing spend wrapper, task shared-service-ab-284.
// This writes real reservations for three deterministic loopback responses, never real inference.
const root = resolve(import.meta.dir, '..');
const expectedScope = createHash('sha256').update('feature:shared-service-ab-284').digest('hex');
const digest = value => createHash('sha256').update(value).digest('hex');
const directory = resolve(root, '.artifacts/migration', `shared-budget-${Date.now()}-${crypto.randomUUID().slice(0, 8)}`);
mkdirSync(directory, { recursive: true });
const cleanEnv = Object.fromEntries(['PATH', 'HOME', 'GOCACHE', 'GOMODCACHE', 'GOPATH', 'CLOUDSDK_CONFIG'].filter(key => process.env[key]).map(key => [key, process.env[key]]));
const report = { status: 'failed', scope: expectedScope, paid_provider_calls: 0, real_ledger: false, snapshots: [], provider_calls: [], evidence: {} };
let h, provider, release;
let assistantStarted = false;
const startedChats = [];

function sourceEvidence() {
  return Object.fromEntries(['core/internal/spend/client.go', 'core/internal/spend/ledger.go', 'core/internal/spend/firestore/ledger.go', 'core/internal/conversation/openai.go', 'core/internal/chat/service.go', 'core/e2e/shared-service.test.js'].map(path => [path, digest(readFileSync(resolve(root, path)))]));
}
function guards() {
  const temporary = resolve(root, 'core/.artifacts');
  mkdirSync(temporary, { recursive: true });
  const folder = mkdtempSync(resolve(temporary, 'shared-budget-guard-'));
  const main = resolve(folder, 'main.go');
  writeFileSync(main, `package main
import("context";"encoding/json";"fmt";"os";"strings";"time";"nori/internal/spend";sf "nori/internal/spend/firestore")
func main(){
 os.Setenv("FIRESTORE_EMULATOR_HOST","127.0.0.1:1")
 _,err:=sf.Open(context.Background(),spend.LedgerConfig{Project:"unused",Database:"unused"})
 emulator:=spend.IsControlError(err)&&strings.Contains(err.Error(),"shared budget cannot use an emulator")
 os.Unsetenv("FIRESTORE_EMULATOR_HOST")
 _,known,unknownErr:=spend.ActualCost("openai",nil,nil)
 now:=time.Now();scope:=spend.ScopeRecord{Profile:"feature",Allowance:250000000,ExpiresAt:now.Add(time.Hour)}
 pool:=spend.PoolRecord{Day:now.UTC().Format("2006-01-02"),Month:now.UTC().Format("2006-01")}
 req,err:=spend.NewReserveRequest("feature","pure-guard-only","openai","gpt-6.1-sol",100,1,"10000000-0000-4000-8000-000000000001");if err!=nil{panic(err)}
 limited:=scope;limited.Used=limited.Allowance-req.Amount+1
 _,_,_,scopeErr:=spend.Admit(req,limited,pool,now)
 daily:=pool;daily.DayKnown=2000000000-req.Amount+1
 _,_,_,dayErr:=spend.Admit(req,scope,daily,now)
 monthly:=pool;monthly.MonthKnown=20000000000-req.Amount+1
 _,_,_,monthErr:=spend.Admit(req,scope,monthly,now)
 values:=map[string]bool{"emulator_refused":emulator,"unknown_cost_is_not_known_zero":unknownErr==nil&&!known,"scope_limit_refused":spend.IsAdmissionDenied(scopeErr),"shared_day_limit_refused":spend.IsAdmissionDenied(dayErr),"shared_month_limit_refused":spend.IsAdmissionDenied(monthErr)}
 for name,pass:=range values{if !pass{panic(fmt.Sprintf("guard failed: %s",name))}}
 json.NewEncoder(os.Stdout).Encode(values)
}
`);
  try {
    const result = spawnSync('go', ['run', main], { cwd: resolve(root, 'core'), env: cleanEnv, encoding: 'utf8', timeout: 120000 });
    assert.equal(result.status, 0, result.stderr);
    return JSON.parse(result.stdout);
  } finally { rmSync(folder, { recursive: true, force: true }); }
}
const reader = `import sys,json
sys.path.insert(0,sys.argv[1])
import keys
scope=sys.argv[2]
value=keys.values(keys.get_scope(scope));pool=keys.values(keys.cloud_api('GET',keys.POOL))
rows=keys.cloud_api('POST',keys.ROOT+':runQuery',{'structuredQuery':{'from':[{'collectionId':'development_spend_reservations'}],'where':{'fieldFilter':{'field':{'fieldPath':'scope'},'op':'EQUAL','value':{'stringValue':scope}}}}})
reservations={}
for row in rows:
 doc=row.get('document')
 if doc:
  v=keys.values(doc);reservations[doc['name'].split('/')[-1]]={k:v.get(k) for k in ('scope','provider','amount_nanos','settled')}
print(json.dumps({'scope':{k:value.get(k) for k in ('profile','allowance_nanos','approved_allowance_nanos','used_nanos','closed','expires_at')},'pool':{k:pool.get(k) for k in ('day','month','day_known_nanos','month_known_nanos','pending_nanos','blocked')},'reservations':reservations}))
`;
function ledger(label) {
  const result = spawnSync('python3', ['-c', reader, resolve(root, 'core/infrastructure/spend'), expectedScope], { env: cleanEnv, encoding: 'utf8', timeout: 120000 });
  assert.equal(result.status, 0, 'Read-only shared ledger snapshot failed; no credentials printed');
  const value = JSON.parse(result.stdout);
  const path = resolve(directory, `${label}.json`);
  writeFileSync(path, JSON.stringify(value, null, 2) + '\n');
  report.snapshots.push({ label, path, sha256: digest(readFileSync(path)) });
  return value;
}
const additional = (before, after) => Object.entries(after.reservations).filter(([id]) => !before.reservations[id]);
function unchangedLimits(before, after) {
  for (const key of ['profile', 'allowance_nanos', 'approved_allowance_nanos', 'closed', 'expires_at']) assert.deepEqual(after.scope[key], before.scope[key], `scope ${key} changed`);
  assert.equal(after.pool.blocked, false);
  assert.equal(after.pool.day, before.pool.day, 'UTC day changed during verification');
  assert.equal(after.pool.month, before.pool.month, 'UTC month changed during verification');
}
async function request(path, body, status = 200) {
  const response = await fetch(h.address + path, { method: body === undefined ? 'GET' : 'POST', headers: { 'content-type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(20000) });
  assert.equal(response.status, status, await response.clone().text());
  return response.json();
}
const assistant = () => request('/v1/snapshot');
const chat = id => request(`/v1/conversations/${id}`);
const idleAssistant = () => until(assistant, value => !value.conversation.pending && !value.conversation.processing, 'assistant completion');
const idleChat = id => until(() => chat(id), value => value.activeTurn === null, 'chat completion');
const wait = ms => new Promise(done => setTimeout(done, ms));
async function main() {
  report.evidence.source_sha256 = sourceEvidence();
  report.evidence.pure_guards = guards();
  report.evidence.unknown_cost = { live_reservation_pinned: false, proof: 'ActualCost(nil) reports unknown; Client.Settle returns before ledger settlement when cost is unknown. Existing shared-service crash E2E closes unknown attempts without resending.', runtime_journey: 'core/e2e/shared-service.test.js: provider crash closes the saved partial reply after its lease expires and never resends' };
  if (process.argv.includes('--guard-only')) { report.status = 'passed'; report.scope_of_proof = 'Pure guards and source evidence only; no real ledger reads or writes'; return; }
  assert.equal(process.env.NORI_SHARED_BUDGET_E2E, '1', 'Explicit NORI_SHARED_BUDGET_E2E=1 required');
  assert.equal(process.env.NORI_SPEND_SCOPE, expectedScope, 'Use only wrapper task shared-service-ab-284');
  assert.equal(process.env.NORI_SPEND_PROFILE, 'feature');
  assert.equal(process.env.NORI_SPEND_DATABASE, 'nori-sandbox');
  assert.equal(process.env.NORI_PROJECT_ID, 'bot-111549');
  assert.ok(!process.env.FIRESTORE_EMULATOR_HOST, 'Use NORI_STATE_EMULATOR_HOST; real shared ledger refuses emulator');
  const emulator = process.env.NORI_STATE_EMULATOR_HOST;
  assert.match(emulator ?? '', /^127\.0\.0\.1:\d+$/);
  assert.match(process.env.TEMPORAL_ADDRESS ?? '', /^127\.0\.0\.1:\d+$/);
  const before = ledger('before');
  assert.equal(before.scope.profile, 'feature');
  assert.equal(before.scope.allowance_nanos, 250000000, 'Original $0.25 allowance required; do not raise it');
  assert.equal(before.scope.closed, false);
  assert.ok(new Date(before.scope.expires_at) > new Date(), 'Existing scope expired');
  assert.equal(before.pool.blocked, false);
  const now = new Date().toISOString();
  assert.equal(before.pool.day, now.slice(0, 10));
  assert.equal(before.pool.month, now.slice(0, 7));
  const available = Math.min(before.scope.allowance_nanos - before.scope.used_nanos, 2000000000 - before.pool.day_known_nanos - before.pool.pending_nanos, 20000000000 - before.pool.month_known_nanos - before.pool.pending_nanos);
  assert.ok(available >= 200000000, 'Need $0.20 existing headroom for normal concurrent reservations; do not raise allowance');
  let held = true;
  const gate = new Promise(done => { release = () => { held = false; done(); }; });
  provider = createServer(async (req, res) => {
    try {
      const chunks = []; for await (const chunk of req) chunks.push(chunk);
      const data = Buffer.concat(chunks); const body = JSON.parse(data.toString());
      assert.equal(body.model, 'gpt-6.1-sol');
      assert.equal(body.reasoning_effort, 'low');
      assert.equal(body.service_tier, 'default');
      report.provider_calls.push({ input_bytes: data.length, output_limit: body.max_completion_tokens, stream: !!body.stream, model: body.model });
      assert.ok(report.provider_calls.length <= 3, 'Three-request bound exceeded');
      if (held) await gate;
      const content = JSON.stringify({ actions: [], text: 'The shared-budget check completed.' });
      const usage = { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, prompt_tokens_details: { cached_tokens: 0 }, completion_tokens_details: { reasoning_tokens: 0 } };
      if (!body.stream) { res.writeHead(200, { 'content-type': 'application/json' }); res.end(JSON.stringify({ model: 'gpt-6.1-sol', service_tier: 'default', usage, choices: [{ message: { role: 'assistant', content }, finish_reason: 'stop' }] })); return; }
      res.writeHead(200, { 'content-type': 'text/event-stream' });
      const emit = value => res.write(`data: ${JSON.stringify({ model: 'gpt-6.1-sol', service_tier: 'default', ...value })}\n\n`);
      emit({ choices: [{ delta: { content }, finish_reason: null }] });
      emit({ choices: [{ delta: {}, finish_reason: 'stop' }] });
      emit({ choices: [], usage }); res.end('data: [DONE]\n\n');
    } catch { res.destroy(); }
  });
  await new Promise(done => provider.listen(0, '127.0.0.1', done));
  h = await createHarness({ emulator, directory, env: { FIRESTORE_EMULATOR_HOST: undefined, NORI_STATE_EMULATOR_HOST: emulator, NORI_SPEND_SCOPE: expectedScope, NORI_SPEND_PROFILE: 'feature', NORI_SPEND_DATABASE: 'nori-sandbox', NORI_E2E_OPENAI_URL: `http://127.0.0.1:${provider.address().port}` } });
  report.real_ledger = true;
  report.app_database = h.database;
  const a = await request('/v1/conversations', { title: 'Shared allowance first chat' }, 201);
  const assistantInput = { id: crypto.randomUUID(), message: 'Please answer this ordinary conversation check directly.' };
  const chatInput = { id: crypto.randomUUID(), text: 'Please answer this independent chat check directly.' };
  assistantStarted = true;
  startedChats.push(a.id);
  await Promise.all([request('/api/turn', assistantInput, 202), request(`/v1/conversations/${a.id}/messages`, chatInput, 202)]);
  await until(async () => report.provider_calls.length, count => count === 2, 'two concurrent provider requests');
  const pending = ledger('two-concurrent-reservations');
  unchangedLimits(before, pending);
  const reservations = additional(before, pending);
  assert.equal(reservations.length, 2);
  const expectedAmounts = report.provider_calls.map(call => (call.input_bytes + 4096) * 2000 + call.output_limit * 10000).sort((a, b) => a - b);
  assert.deepEqual(reservations.map(([, value]) => value.amount_nanos).sort((a, b) => a - b), expectedAmounts);
  for (const [, value] of reservations) assert.deepEqual([value.scope, value.provider, value.settled], [expectedScope, 'openai', false]);
  const reserved = reservations.reduce((sum, [, value]) => sum + value.amount_nanos, 0);
  assert.equal(pending.scope.used_nanos - before.scope.used_nanos, reserved);
  assert.equal(pending.pool.pending_nanos - before.pool.pending_nanos, reserved);
  assert.equal(pending.pool.day_known_nanos, before.pool.day_known_nanos);
  assert.equal(pending.pool.month_known_nanos, before.pool.month_known_nanos);
  release();
  const [assistantDone, chatDone] = await Promise.all([idleAssistant(), idleChat(a.id)]);
  assert.equal(chatDone.messages.at(-1).status, 'completed');
  assert.equal(assistantDone.runs.length, 0);
  const settled = ledger('two-known-settlements');
  unchangedLimits(before, settled);
  assert.equal(additional(before, settled).length, 2);
  assert.ok(additional(before, settled).every(([, value]) => value.settled));
  assert.equal(settled.scope.used_nanos - before.scope.used_nanos, 24000);
  assert.equal(settled.pool.day_known_nanos - before.pool.day_known_nanos, 24000);
  assert.equal(settled.pool.month_known_nanos - before.pool.month_known_nanos, 24000);
  assert.equal(settled.pool.pending_nanos, before.pool.pending_nanos);
  await h.stop(); await h.start();
  await request('/api/turn', assistantInput, 202);
  await request(`/v1/conversations/${a.id}/messages`, chatInput, 202);
  await request(`/v1/conversations/${a.id}/messages`, { ...chatInput, text: 'Conflicting retry.' }, 409);
  assert.deepEqual((await idleAssistant()).messages, assistantDone.messages);
  assert.deepEqual(await idleChat(a.id), chatDone);
  await wait(250);
  assert.equal(report.provider_calls.length, 2);
  const retried = ledger('same-input-retries-after-restart');
  assert.deepEqual(retried, settled);
  const b = await request('/v1/conversations', { title: 'A new ID keeps the same allowance' }, 201);
  assert.notEqual(b.id, a.id);
  startedChats.push(b.id);
  await request(`/v1/conversations/${b.id}/messages`, { id: crypto.randomUUID(), text: 'A new chat still shares this development allowance.' }, 202);
  await idleChat(b.id);
  const final = ledger('new-chat-same-scope');
  unchangedLimits(before, final);
  assert.equal(report.provider_calls.length, 3);
  const all = additional(before, final);
  assert.equal(all.length, 3);
  assert.ok(all.every(([, value]) => value.scope === expectedScope && value.provider === 'openai' && value.settled));
  assert.equal(final.scope.used_nanos - before.scope.used_nanos, 36000);
  assert.equal(final.pool.day_known_nanos - before.pool.day_known_nanos, 36000);
  assert.equal(final.pool.month_known_nanos - before.pool.month_known_nanos, 36000);
  assert.equal(final.pool.pending_nanos, before.pool.pending_nanos);
  assert.equal(h.sent.length, 0);
  report.ledger_accounted_synthetic_usd = 0.000036;
  report.remaining = ['Unknown-cost retention is source/pure-guard plus existing runtime crash evidence; no real unknown reservation was intentionally pinned.', 'Real ledger cap exhaustion was not attempted; pure production Admit guards verify existing scope/day/month refusals.'];
  report.status = 'passed';
}
try { await main(); }
catch (error) { report.error = error instanceof Error ? error.message : 'Shared-budget verification failed'; process.exitCode = 1; }
finally {
  release?.();
  if (h && report.status !== 'passed') {
    // Let already-admitted deterministic responses settle normally, even when an assertion fails.
    // Never refund, reset, or fabricate a settlement to make cleanup pass.
    try {
      await Promise.all([...(assistantStarted ? [idleAssistant()] : []), ...startedChats.map(idleChat)]);
      ledger('failure-cleanup-observation');
    } catch { report.cleanup_observation = 'Could not confirm all admitted requests settled; inspect retained ledger before another run'; }
  }
  if (h) { writeFileSync(resolve(directory, 'api.log'), h.logs); await h.close(); }
  if (provider) { provider.closeAllConnections(); await new Promise(done => provider.close(done)); }
  const path = resolve(directory, 'report.json');
  writeFileSync(path, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify({ status: report.status, evidence: path, real_ledger: report.real_ledger, paid_provider_calls: 0 }));
}
