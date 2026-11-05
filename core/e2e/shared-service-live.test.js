import { beforeAll, afterAll, test, expect } from 'bun:test';
import { createHarness, until } from './shared-service-harness';
import { follow } from '../../chat/src/contract';

// Paid acceptance, only through infrastructure/spend/run.py with one logical chat ID.
// App records stay on the emulator; the spend ledger stays on its real shared database.
const enabled = process.env.NORI_PROVIDER_SMOKE === '1';
const journey = enabled ? test : test.skip;
let h, firstPassed = false;
const headers = { 'content-type': 'application/json' };
async function post(path, value, status = 202) {
  const response = await fetch(h.address + path, { method: 'POST', headers, body: JSON.stringify(value), signal: AbortSignal.timeout(15000) });
  const body = await response.json();
  expect(response.status).toBe(status);
  return body;
}
const snapshot = () => fetch(h.address + '/v1/snapshot').then(r => r.json());
const chat = id => fetch(h.address + '/v1/conversations/' + id).then(r => r.json());
beforeAll(async () => {
  if (!enabled) return;
  for (const name of ['NORI_SPEND_SCOPE', 'NORI_SPEND_PROFILE', 'OPENAI_API_KEY', 'OPENROUTER_API_KEY']) {
    if (!process.env[name]) throw new Error('Run through the development spend wrapper; missing ' + name);
  }
  if (process.env.FIRESTORE_EMULATOR_HOST) throw new Error('Paid calls must use the real spend ledger, not FIRESTORE_EMULATOR_HOST');
  h = await createHarness({ emulator: process.env.NORI_STATE_EMULATOR_HOST });
  await until(snapshot, s => !s.conversation.pending && !s.conversation.processing, 'deterministic introduction');
  await h.stop();
  delete h.env.FIRESTORE_EMULATOR_HOST;
  h.env.NORI_STATE_EMULATOR_HOST = process.env.NORI_STATE_EMULATOR_HOST;
  for (const name of ['NORI_SPEND_SCOPE', 'NORI_SPEND_PROFILE', 'NORI_SPEND_DATABASE', 'OPENAI_API_KEY', 'OPENROUTER_API_KEY']) {
    if (process.env[name]) h.env[name] = process.env[name];
  }
  delete h.env.NORI_E2E_OPENAI_URL;
  delete h.env.NORI_E2E_MODEL_URL;
  // Synthetic read/write services and classifier remain loopback. Models are real.
  h.env.NORI_LOCAL_COMPUTER_URL = h.provider;
  await h.start();
  await h.startWorker();
  const capability = await fetch(h.address + '/v1/capabilities').then(r => r.json());
  expect(capability.deterministic).toBe(false);
}, 180000);
afterAll(async () => {
  if (!h) return;
  console.log(JSON.stringify({ appDatabase: h.database, retainedEvidence: h.directory }));
  const state = await snapshot().catch(() => ({ runs: [] }));
  for (const run of state.runs.filter(r => r.status === 'running')) {
    await post('/api/command', { id: crypto.randomUUID(), run_id: run.id, kind: 'stop' }).catch(() => {});
  }
  await h.close();
}, 30000);

journey('selected Sol streams a saved answer after reading its assigned CSV through the service', async () => {
  const c = await post('/v1/conversations', { title: 'Provider smoke' }, 201);
  const upload = crypto.randomUUID();
  const response = await fetch(`${h.address}/v1/conversations/${c.id}/uploads?id=${upload}&filename=amounts.csv`, {
    method: 'POST', headers: { 'content-type': 'text/csv' }, body: 'item,amount\napples,17\npears,25\n',
  });
  expect(response.status).toBe(200);
  const controller = new AbortController(), events = [];
  const stream = follow(c.id, 0, controller.signal, event => events.push(event), h.address).catch(error => {
    if (!controller.signal.aborted) throw error;
  });
  try {
    await post(`/v1/conversations/${c.id}/messages`, { id: crypto.randomUUID(), text: 'Read the attached CSV using upload_read, add the amount column, and answer in one short sentence. Do not use any other tool.', attachments: [upload] });
    const final = await until(() => chat(c.id), c => c.activeTurn === null, 'paid CSV reply', 120000);
    expect(final.messages.at(-1).status).toBe('completed');
    expect(final.messages.at(-1).text).toContain('42');
    expect(final.messages.at(-1).operations.some(op => op.name === 'upload_read' && op.outcome === 'completed')).toBe(true);
    await until(async () => events, rows => rows.some(e => e.kind === 'turn.completed'), 'streamed final');
    expect(events.some(e => e.kind === 'reply.updated' && e.messages.at(-1)?.text?.length > 0)).toBe(true);
    expect((await chat(c.id)).messages).toEqual(final.messages);
    firstPassed = true;
    console.log(JSON.stringify({ provider: 'openai', model: 'gpt-6.1-sol', streamed: true, tool: 'upload_read', answer: final.messages.at(-1).text }));
  } finally { controller.abort(); await stream; }
}, 150000);

journey('selected Terra reaches a durable task question through the existing assistant worker', async () => {
  if (!firstPassed) throw new Error('First provider scenario failed; no further paid calls');
  const before = new Set((await snapshot()).runs.map(r => r.id));
  await post('/api/turn', { id: crypto.randomUUID(), message: 'Start a background task for this exact request: ask me which CSV column I want to total, using request_input, then wait for my answer. Do not answer the question in this conversation. Do not use the computer, calendar, email, search or any external action.' });
  const state = await until(snapshot, s => s.runs.some(r => !before.has(r.id) && r.question), 'paid inner task question', 150000);
  const run = state.runs.find(r => !before.has(r.id) && r.question);
  expect(run.question.text || run.question.prompt || JSON.stringify(run.question)).toMatch(/column/i);
  const trace = await post('/api/trace', { run_id: run.id }, 200);
  expect(trace.steps.some(step => step.model === 'openai/gpt-6.1-terra' && step.status === 'completed' && step.calls?.some(call => call.name === 'request_input'))).toBe(true);
  await post('/api/command', { id: crypto.randomUUID(), run_id: run.id, kind: 'stop' });
  await until(snapshot, s => s.runs.find(r => r.id === run.id)?.status === 'cancelled', 'smoke task cleanup', 30000);
  console.log(JSON.stringify({ provider: 'openrouter', model: 'openai/gpt-6.1-terra', tool: 'request_input', stopped: true }));
}, 210000);
