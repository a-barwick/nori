import { afterAll, beforeAll, expect, test } from 'bun:test';
import { createFixture } from './fixture';
import { CONTRACT, PREFIX, follow, request, type Conversation, type Event } from './contract';

// Default: local fixture. A future isolated service can opt into this draft contract.
// Neither mode certifies the full migration. No production or paid providers are allowed.
const external = process.env.MIGRATION_CONTRACT_URL;
if (external) {
  const url = new URL(external);
  if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1' || url.pathname !== '/' || url.search || url.hash || url.username || url.password) throw new Error('Contract target must be a loopback HTTP origin');
}
const fixture = external ? null : createFixture(12);
const server = fixture ? Bun.serve({ hostname: '127.0.0.1', port: 0, fetch: fixture.handle }) : null;
const base = external?.replace(/\/$/, '') ?? `http://127.0.0.1:${server!.port}`;
const controllers: AbortController[] = [];
beforeAll(async () => {
  const capabilities = await request<{ contract: string; mode: string; deterministic: boolean }>('/capabilities', {}, base);
  expect(capabilities.contract).toBe(CONTRACT);
  expect(capabilities.mode).toBe(external ? 'isolated-test' : 'fixture');
  expect(capabilities.deterministic).toBe(true);
});
afterAll(() => { for (const c of controllers) c.abort(); fixture?.close(); server?.stop(true); });
const create = () => request<Conversation>('/conversations', { method: 'POST' }, base);
const snapshot = (id: string) => request<Conversation>(`/conversations/${id}`, {}, base);
const send = (id: string, message: string, messageID = crypto.randomUUID()) => request<{ turn_id: string }>(`/conversations/${id}/turns`, { method: 'POST', body: JSON.stringify({ id: messageID, message }) }, base);
async function until(read: () => Promise<Conversation>, accept: (c: Conversation) => boolean) {
  const end = Date.now() + 5000;
  while (Date.now() < end) { const c = await read(); if (accept(c)) return c; await Bun.sleep(10); }
  throw new Error('Journey timed out');
}
function watch(id: string, after: number) {
  const controller = new AbortController(); controllers.push(controller);
  const events: Event[] = [];
  const done = follow(id, after, controller.signal, e => events.push(e), base).catch(e => { if (!controller.signal.aborted) throw e; });
  return { events, close: async () => { controller.abort(); await done; } };
}

test('test target identifies its contract, execution mode and persistence boundary', async () => {
  const capabilities = await request<{ contract: string; mode: string; persistence: string }>('/capabilities', {}, base);
  expect(capabilities.contract).toBe(CONTRACT);
  expect(capabilities.mode).toBe(external ? 'isolated-test' : 'fixture');
  expect(capabilities.persistence).toBe(external ? 'durable' : 'process-memory');
});
test('two conversations stream independently and reload returns the same saved fixture reply', async () => {
  const a = await create(), b = await create();
  await Promise.all([send(a.id, 'alpha-private'), send(b.id, 'beta-private')]);
  const [one, two] = await Promise.all([until(() => snapshot(a.id), c => !c.activeTurn), until(() => snapshot(b.id), c => !c.activeTurn)]);
  expect(one.messages).toHaveLength(2); expect(two.messages).toHaveLength(2);
  expect(JSON.stringify(one)).not.toContain('beta-private'); expect(JSON.stringify(two)).not.toContain('alpha-private');
  expect(await snapshot(a.id)).toEqual(one);
});
test('repeated input IDs do not generate again; conflicting content is refused', async () => {
  const c = await create(), id = crypto.randomUUID();
  const first = await send(c.id, 'once', id);
  expect(await send(c.id, 'once', id)).toEqual(first);
  await expect(send(c.id, 'different', id)).rejects.toThrow('409');
  const finished = await until(() => snapshot(c.id), c => !c.activeTurn);
  expect(await send(c.id, 'once', id)).toEqual(first);
  expect((await snapshot(c.id)).messages).toEqual(finished.messages);
});
test('disconnect and cursor replay converge once to the final answer', async () => {
  const c = await create();
  const first = watch(c.id, 0);
  await send(c.id, 'reconnect this conversation');
  await until(() => snapshot(c.id), c => c.position > 3);
  await first.close();
  expect(first.events.length).toBeGreaterThan(0);
  const cursor = first.events.at(-1)!.seq;
  const final = await until(() => snapshot(c.id), c => !c.activeTurn);
  const second = watch(c.id, cursor);
  const end = Date.now() + 3000;
  while (second.events.at(-1)?.seq !== final.position && Date.now() < end) await Bun.sleep(10);
  await second.close();
  const all = [...first.events, ...second.events];
  expect(all.map(e => e.seq)).toEqual(Array.from({ length: final.position }, (_, i) => i + 1));
  expect(all.filter(e => e.kind === 'turn.completed')).toHaveLength(1);
  expect(second.events.at(-1)!.conversation).toEqual(final);
});
test('targeted stop preserves partial text while the other conversation completes', async () => {
  const a = await create(), b = await create();
  const [turnA, turnB] = await Promise.all([send(a.id, 'stop this answer'), send(b.id, 'keep this other answer running')]);
  await until(() => snapshot(a.id), c => c.messages.at(-1)!.text.length > 0);
  const cancel = (id: string, turn: string) => request<Conversation>(`/conversations/${id}/cancel`, { method: 'POST', body: JSON.stringify({ turn_id: turn }) }, base);
  await expect(cancel(a.id, turnB.turn_id)).rejects.toThrow('409');
  const stopped = await cancel(a.id, turnA.turn_id);
  expect(stopped.messages.at(-1)!.status).toBe('cancelled');
  expect(stopped.messages.at(-1)!.text.length).toBeGreaterThan(0);
  await until(() => snapshot(b.id), c => !c.activeTurn);
  expect(await snapshot(a.id)).toEqual(stopped);
  expect((await snapshot(b.id)).messages.at(-1)!.status).toBe('completed');
});
test('invalid input, unknown conversations and cross-origin writes fail explicitly', async () => {
  const c = await create();
  await expect(send(c.id, '')).rejects.toThrow('400');
  await expect(snapshot(crypto.randomUUID())).rejects.toThrow('404');
  const badCursor = await fetch(`${base}${PREFIX}/conversations/${c.id}/events?after=999`);
  expect(badCursor.status).toBe(400);
  const foreign = await fetch(`${base}${PREFIX}/conversations`, { method: 'POST', headers: { Origin: 'https://example.org' } });
  expect(foreign.status).toBe(403);
});
