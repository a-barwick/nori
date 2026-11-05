// Explicit offline HTTP fixture. No models, tools, credentials or durable storage.
import { CONTRACT, PREFIX, type Conversation, type Event } from './contract.ts';

export function createFixture(delay = 80) {
  const conversations = new Map<string, Conversation>();
  const events = new Map<string, Event[]>();
  const listeners = new Map<string, Set<(event: Event) => void>>();
  const receipts = new Map<string, { message: string; turn: string }>();
  const timers = new Set<ReturnType<typeof setTimeout>>();
  const json = (value: unknown, status = 200) => Response.json(value, { status, headers: { 'Cache-Control': 'no-store' } });
  const publish = (c: Conversation, kind: Event['kind']) => {
    c.position++;
    const event: Event = { seq: c.position, kind, conversation: structuredClone(c) };
    events.get(c.id)!.push(event);
    for (const listener of listeners.get(c.id)!) listener(event);
  };
  function later(fn: () => void) {
    const timer = setTimeout(() => { timers.delete(timer); fn(); }, delay);
    timers.add(timer);
  }
  async function handle(request: Request): Promise<Response> {
    const url = new URL(request.url);
    if (!['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname)) return json({ error: 'Loopback only' }, 403);
    if (request.headers.get('origin') && request.headers.get('origin') !== url.origin) return json({ error: 'Cross-origin request' }, 403);
    if (!url.pathname.startsWith(PREFIX + '/')) return json({ error: 'Not found' }, 404);
    const path = url.pathname.slice(PREFIX.length);
    if (path === '/capabilities' && request.method === 'GET') return json({ contract: CONTRACT, service: 'nori', fixture: true, isolated: true, deterministic: true, persistence: 'process-memory', conversationModes: ['chat'] });
    if (path === '/conversations') {
      if (request.method === 'GET') return json({ conversations: [...conversations.values()] });
      if (request.method !== 'POST') return json({ error: 'Method not allowed' }, 405);
      const c: Conversation = { id: crypto.randomUUID(), title: 'New conversation', mode: 'chat', messages: [], activeTurn: null, position: 0 };
      conversations.set(c.id, c); events.set(c.id, []); listeners.set(c.id, new Set());
      return json(c, 201);
    }
    const match = path.match(/^\/conversations\/([a-z0-9-]+)(?:\/(messages|cancel|events))?$/);
    const c = match && conversations.get(match[1]);
    if (!c) return json({ error: 'Not found' }, 404);
    const operation = match![2];
    if (!operation && request.method === 'GET') return json(c);
    if (operation === 'events' && request.method === 'GET') {
      const after = Number(url.searchParams.get('after') ?? 0);
      if (!Number.isSafeInteger(after) || after < 0 || after > c.position) return json({ error: 'Invalid cursor' }, 400);
      const encoder = new TextEncoder();
      let cleanup = () => {};
      const stream = new ReadableStream<Uint8Array>({
        start(controller) {
          const send = (event: Event) => controller.enqueue(encoder.encode(`id: ${event.seq}\ndata: ${JSON.stringify(event)}\n\n`));
          const close = () => { listeners.get(c.id)?.delete(send); request.signal.removeEventListener('abort', close); try { controller.close(); } catch { /* already closed */ } };
          cleanup = close;
          if (request.signal.aborted) { close(); return; }
          for (const event of events.get(c.id)!) if (event.seq > after) send(event);
          listeners.get(c.id)!.add(send);
          request.signal.addEventListener('abort', close, { once: true });
        },
        cancel() { cleanup(); },
      });
      return new Response(stream, { headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' } });
    }
    if (request.method !== 'POST') return json({ error: 'Method not allowed' }, 405);
    let body;
    try { body = await request.json(); } catch { return json({ error: 'Invalid JSON' }, 400); }
    if (operation === 'cancel') {
      if (!body || typeof body.turnId !== 'string' || c.activeTurn !== body.turnId) return json({ error: 'Turn is not active here' }, 409);
      c.messages.at(-1)!.status = 'cancelled'; c.activeTurn = null;
      publish(c, 'turn.cancelled'); return json(c);
    }
    if (operation !== 'messages') return json({ error: 'Not found' }, 404);
    if (!body || typeof body.id !== 'string' || !/^[a-f0-9-]{36}$/.test(body.id) || typeof body.text !== 'string' || !body.text.trim() || body.text.length > 4000) return json({ error: 'Invalid message' }, 400);
    const key = `${c.id}:${body.id}`;
    const receipt = receipts.get(key);
    if (receipt) return receipt.message === body.text ? json({ turnId: receipt.turn, conversation: c }, 202) : json({ error: 'ID conflict' }, 409);
    if (c.activeTurn) return json({ error: 'Conversation busy' }, 409);
    const turn = crypto.randomUUID();
    receipts.set(key, { message: body.text, turn });
    c.title = body.text.slice(0, 48); c.activeTurn = turn;
    c.messages.push({ id: body.id, role: 'user', text: body.text, status: 'completed' });
    const answer = { id: crypto.randomUUID(), role: 'assistant' as const, text: '', status: 'streaming' as 'streaming' | 'completed' };
    c.messages.push(answer);
    const words = `Fixture reply to “${body.text}”. This is a scripted response. No model or external tool was called.`.split(' ');
    publish(c, 'reply.updated');
    const next = () => {
      if (c.activeTurn !== turn) return;
      answer.text += (answer.text ? ' ' : '') + words.shift();
      if (words.length) { publish(c, 'reply.updated'); later(next); }
      else { answer.status = 'completed'; c.activeTurn = null; publish(c, 'turn.completed'); }
    };
    later(next);
    return json({ turnId: turn, conversation: c }, 202);
  }
  return { handle, close() { for (const timer of timers) clearTimeout(timer); timers.clear(); } };
}
