import { afterAll, beforeAll, expect, test } from 'bun:test';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';
import { isAbsolute, relative, resolve } from 'node:path';
import { createHarness, until } from '../core/e2e/shared-service-harness';

// Unpaid retained-data rehearsal. Both executables use one unique emulator database and file directory.
// NORI_SHARED_RECOVERY_E2E=1 NORI_RECOVERY_OLD_BINARY=/absolute/path/nori-local-051960d
const journey = process.env.NORI_SHARED_RECOVERY_E2E === '1' ? test : test.skip;
const live = process.env.NORI_SHARED_RECOVERY_E2E === '1';
const owner = 'owners/austinbarwickdavis@gmail.com';
const headers = { 'content-type': 'application/json' };
const hash = (data) => createHash('sha256').update(data).digest('hex');
const ids = Object.fromEntries(['message', 'task', 'finished', 'question', 'upload', 'result', 'schedule'].map(key => [key, crypto.randomUUID()]));
const csv = Buffer.from('name,value\nlegacy-retained,17\n');
const pdf = Buffer.from('%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n');
let h, proxy, directory, oldBinary;
let report = { status: 'failed', scope: 'Isolated deterministic upgrade and rollback; no production or paid providers', stages: [], blocked_https: [] };
let paths = [];
let liveTaskID;

function field(value) {
  if (value instanceof Date) return { timestampValue: value.toISOString() };
  if (Buffer.isBuffer(value)) return { bytesValue: value.toString('base64') };
  if (Array.isArray(value)) return { arrayValue: { values: value.map(field) } };
  if (value === null) return { nullValue: null };
  if (typeof value === 'object') return { mapValue: { fields: Object.fromEntries(Object.entries(value).map(([k, v]) => [k, field(v)])) } };
  if (typeof value === 'number') return Number.isInteger(value) ? { integerValue: String(value) } : { doubleValue: value };
  if (typeof value === 'boolean') return { booleanValue: value };
  return { stringValue: value };
}
async function seed(path, data) {
  const response = await fetch(`${h.documents}/${owner}/${path}`, { method: 'PATCH', headers, body: JSON.stringify({ fields: field(data).mapValue.fields }) });
  expect(response.status, await response.clone().text()).toBe(200);
  paths.push(path);
}
async function raw(path) {
  const response = await fetch(`${h.documents}/${owner}/${path}`);
  expect(response.status, await response.clone().text()).toBe(200);
  return (await response.json()).fields;
}
async function collection(path) {
  const response = await fetch(`${h.documents}/${owner}/${path}?pageSize=1000`);
  expect(response.status).toBe(200);
  const body = await response.json();
  expect(body.nextPageToken).toBeUndefined();
  return Object.fromEntries((body.documents ?? []).map(doc => [doc.name.split('/').at(-1), doc.fields]));
}
function files() {
  const root = h.env.NORI_LOCAL_ARTIFACT_DIR;
  const result = {};
  const walk = folder => {
    if (!existsSync(folder)) return;
    for (const name of readdirSync(folder)) {
      const path = resolve(folder, name);
      if (statSync(path).isDirectory()) walk(path);
      else result[relative(root, path)] = hash(readFileSync(path));
    }
  };
  walk(root);
  return result;
}
async function inventory() {
  return { records: Object.fromEntries(await Promise.all(paths.map(async path => [path, await raw(path)]))), files: files() };
}
async function record(stage) {
  const value = await inventory();
  const path = resolve(directory, `${stage}.json`);
  writeFileSync(path, JSON.stringify(value, null, 2) + '\n');
  writeFileSync(resolve(directory, `${stage}.log`), h.logs);
  report.stages.push({ stage, inventory: path, sha256: hash(readFileSync(path)) });
  return value;
}
async function request(path, body, status = 200) {
  const response = await fetch(h.address + path, { method: body === undefined ? 'GET' : 'POST', headers, body: body === undefined ? undefined : JSON.stringify(body) });
  expect(response.status, await response.clone().text()).toBe(status);
  return response.json();
}
const snapshot = () => request('/v1/snapshot');
const idle = () => until(snapshot, value => !value.conversation.pending && !value.conversation.processing, 'idle assistant');
async function assertLegacy(expected) {
  const current = await idle();
  expect(current.messages.find(message => message.id === ids.message)?.text).toBe('A retained answer from the previous release.');
  expect(current.runs.find(run => run.id === ids.task)?.question).toEqual(expected.runs.find(run => run.id === ids.task).question);
  expect(current.runs.find(run => run.id === ids.task)?.uploads).toEqual(expected.runs.find(run => run.id === ids.task).uploads);
  expect(current.runs.find(run => run.id === ids.finished)).toEqual(expected.runs.find(run => run.id === ids.finished));
  expect(current.schedules).toEqual(expected.schedules);
  if (liveTaskID) {
    const task = current.runs.find(run => run.id === liveTaskID);
    expect(task.status).toBe('running');
    expect(task.question).toEqual(expected.runs.find(run => run.id === liveTaskID).question);
  }
  const upload = await fetch(`${h.address}/v1/uploads/${ids.upload}`);
  expect(upload.status).toBe(200);
  expect(Buffer.from(await upload.arrayBuffer())).toEqual(csv);
  const result = await fetch(`${h.address}/v1/results/${ids.result}/file`);
  expect(result.status).toBe(200);
  expect(Buffer.from(await result.arrayBuffer())).toEqual(pdf);
  expect(h.sent).toHaveLength(0);
  return current;
}

beforeAll(async () => {
  if (!live) return;
  oldBinary = process.env.NORI_RECOVERY_OLD_BINARY;
  if (!oldBinary || !isAbsolute(oldBinary) || !existsSync(oldBinary)) throw new Error('NORI_RECOVERY_OLD_BINARY must name the prebuilt compatible previous executable');
  directory = resolve(import.meta.dir, '../.artifacts/migration', `recovery-${Date.now()}-${crypto.randomUUID().slice(0, 8)}`);
  mkdirSync(directory, { recursive: true });
  // The previous executable predates the local Jev override. Reject its HTTPS at loopback,
  // while Go bypasses the proxy for the emulators and deterministic provider on 127.0.0.1.
  proxy = createServer((_req, res) => { res.writeHead(502); res.end(); });
  proxy.on('connect', (req, socket) => { report.blocked_https.push(req.url); socket.end('HTTP/1.1 502 Isolated test denies external traffic\r\nConnection: close\r\n\r\n'); });
  await new Promise(done => proxy.listen(0, '127.0.0.1', done));
  const proxyURL = `http://127.0.0.1:${proxy.address().port}`;
  h = await createHarness({ directory, env: { HTTPS_PROXY: proxyURL, HTTP_PROXY: proxyURL, NO_PROXY: '127.0.0.1,localhost' } });
  await h.stop();
  report = { ...report, database: h.database, directory, previous_binary: { path: oldBinary, sha256: hash(readFileSync(oldBinary)) }, new_binary_sha256: hash(readFileSync(h.binary)), ids };
  const then = new Date(Date.now() - 86400000);
  const due = new Date('2099-01-05T18:00:00Z');
  const attachment = { id: ids.upload, name: 'retained.csv', media_type: 'text/csv', size: csv.length };
  await seed(`messages/${ids.message}`, { id: ids.message, turn_id: '', run_id: '', role: 'assistant', text: 'A retained answer from the previous release.', created_at: then });
  await seed(`uploads/${ids.upload}`, { ...attachment, sha256: hash(csv), created_at: then });
  await seed(`runs/${ids.task}`, { id: ids.task, request: 'Retained task awaiting the exact approval.', status: 'running', waiting: 'approval', started_at: then, position: 1, revision: 1, incorporated: 1, temporal: true, protocol_version: 2, workflow_version: 3, uploads: [attachment], events: [], artifacts: [], question: { id: ids.question, kind: 'approval', text: 'Send the retained email to retained@example.test?', revision: 1, call_id: 'retained-email' } });
  await seed(`runs/${ids.finished}`, { id: ids.finished, request: 'Retained completed report.', status: 'completed', started_at: then, finished_at: then, position: 1, revision: 1, incorporated: 1, result: 'The report is retained.', events: [], artifacts: [] });
  const history = Buffer.from('{"role":"assistant","parts":[{"text":"Retained opaque task history."}]}');
  const reference = hash(Buffer.concat([Buffer.from(ids.finished), Buffer.from([0]), Buffer.from([0]), history]));
  await seed(`runs/${ids.finished}/history/${reference}`, { run_id: ids.finished, parent: '', data: history, depth: 1, bytes: history.length });
  await seed(`results/${ids.result}`, { id: ids.result, root_id: ids.result, version: 1, run_id: ids.finished, revision: 1, kind: 'file', title: 'Retained report', name: 'retained.pdf', media_type: 'application/pdf', size: pdf.length, created_at: then });
  await seed(`schedules/${ids.schedule}`, { id: ids.schedule, kind: 'reminder', text: 'A retained future reminder.', status: 'active', rule: { repeat: 'once', start: '2099-01-05', time: '10:00' }, time_zone: 'America/Los_Angeles', next_at: due, armed_for: due, runs: 0, source_input_id: crypto.randomUUID(), source_text: 'Remind me at ten on January fifth.', created_at: then, updated_at: then });
  for (const [folder, id, data] of [['uploads', ids.upload, csv], ['results', ids.result, pdf]]) {
    mkdirSync(resolve(h.env.NORI_LOCAL_ARTIFACT_DIR, folder), { recursive: true });
    writeFileSync(resolve(h.env.NORI_LOCAL_ARTIFACT_DIR, folder, id), data, { mode: 0o600 });
  }
}, 180000);
afterAll(async () => {
  if (h) { writeFileSync(resolve(directory, 'last-api.log'), h.logs); await h.close(); }
  if (proxy) { proxy.closeAllConnections(); await new Promise(done => proxy.close(done)); }
  if (directory) writeFileSync(resolve(directory, 'report.json'), JSON.stringify(report, null, 2) + '\n');
}, 30000);

journey('repeated upgrades and rollback preserve legacy IDs, pending approval, history, files, schedules and new chat writes', async () => {
  await h.start(oldBinary);
  await h.startWorker();
  await request('/api/turn', { id: crypto.randomUUID(), message: 'Start a background task to ask me for the missing detail.' }, 202);
  const parked = await until(snapshot, value => value.runs.some(run => run.id !== ids.task && run.status === 'running' && run.question?.kind === 'input' && run.incorporated === run.position), 'real legacy task to park');
  liveTaskID = parked.runs.find(run => run.id !== ids.task && run.status === 'running').id;
  report.live_task_id = liveTaskID;
  paths.push(`runs/${liveTaskID}`);
  const legacy = await idle();
  await assertLegacy(legacy);
  const before = await record('previous-release');
  await h.stop();
  for (let attempt = 1; attempt <= 2; attempt++) {
    await h.start();
    const metadata = await request('/v1/capabilities');
    expect(metadata).toMatchObject({ fixture: false, persistence: 'durable', isolated: true, deterministic: true });
    const current = await assertLegacy(legacy);
    const mapped = await request('/v1/conversations/assistant');
    expect(mapped.session.messages).toEqual(current.messages);
    expect(mapped.session.runs).toEqual(current.runs);
    expect(await record(`upgrade-${attempt}`)).toEqual(before);
    if (attempt === 1) await h.stop();
  }
  const chat = await request('/v1/conversations', { title: 'Retained across rollback' }, 201);
  const chatBytes = Buffer.from('name,value\nnew-chat-retained,29\n');
  const upload = await fetch(`${h.address}/v1/conversations/${chat.id}/uploads?id=${ids.upload}&filename=chat.csv`, { method: 'POST', headers: { 'content-type': 'text/csv' }, body: chatBytes });
  expect(upload.status).toBe(200);
  const input = { id: crypto.randomUUID(), text: 'Keep this new conversation through the release change.', attachments: [ids.upload] };
  await request(`/v1/conversations/${chat.id}/messages`, input, 202);
  const saved = await until(() => request(`/v1/conversations/${chat.id}`), value => value.activeTurn === null, 'saved chat reply');
  expect(saved.messages.at(-1).text).toBe(`Reply: ${input.text}`);
  const chatRaw = { document: await raw(`chats/${chat.id}`), events: await collection(`chats/${chat.id}/events`) };
  const retainedFiles = files();
  await record('chat-written');
  const calls = h.requests.length;
  await h.stop('SIGKILL');
  await h.start();
  await request(`/v1/conversations/${chat.id}/messages`, input, 202);
  expect(await request(`/v1/conversations/${chat.id}`)).toEqual(saved);
  expect(h.requests.length).toBe(calls);
  await assertLegacy(legacy);
  await record('new-release-restarted');
  await h.stop();
  await h.start(oldBinary);
  expect((await fetch(`${h.address}/v1/conversations/${chat.id}`)).status).toBe(404);
  await assertLegacy(legacy);
  const oldInput = { id: crypto.randomUUID(), message: 'Can you still reply after the release change?' };
  await request('/api/turn', oldInput, 202);
  const oldSession = await idle();
  const user = oldSession.messages.find(message => message.id === `${oldInput.id}-user`);
  expect(oldSession.messages.find(message => message.turn_id === user.turn_id && message.role === 'assistant')?.text).toBe(`Noted: ${oldInput.message}`);
  expect(await raw(`chats/${chat.id}`)).toEqual(chatRaw.document);
  expect(await collection(`chats/${chat.id}/events`)).toEqual(chatRaw.events);
  expect(files()).toEqual(retainedFiles);
  await assertLegacy(legacy);
  await record('rolled-back-with-new-data');
  await h.stop();
  await h.start();
  expect(await request(`/v1/conversations/${chat.id}`)).toEqual(saved);
  expect(await raw(`chats/${chat.id}`)).toEqual(chatRaw.document);
  expect(await collection(`chats/${chat.id}/events`)).toEqual(chatRaw.events);
  const restoredUpload = await fetch(`${h.address}/v1/conversations/${chat.id}/uploads/${ids.upload}`);
  expect(restoredUpload.status).toBe(200);
  expect(Buffer.from(await restoredUpload.arrayBuffer())).toEqual(chatBytes);
  await assertLegacy(legacy);
  expect((await inventory()).records).toEqual(before.records);
  expect(files()).toEqual(retainedFiles);
  expect(report.blocked_https.length).toBeGreaterThan(0);
  expect(report.blocked_https.every(target => target === 'api.typesafe.ai:443')).toBe(true);
  await record('upgraded-again');
  report.status = 'passed';
}, 180000);
