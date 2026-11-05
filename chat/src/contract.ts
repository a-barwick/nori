// HTTP contract between the Go service, the chat client and the offline fixture.
export const CONTRACT = 'nori-chat-2';
export const PREFIX = '/v1';
export type Attachment = { id: string; name: string; media_type: string; size: number };
export type Capabilities = { contract: string; service: string; fixture: boolean; isolated: boolean; deterministic?: boolean; persistence?: string; provider?: string; model?: string; conversationModes: string[] };
export type Operation = { id: string; call_id: string; name: string; effect_class: string; outcome: string; action?: { digest: string; text: string; question_id: string; approved_by?: string; spec: Record<string, string> }; result?: Record<string, unknown> };
export type Message = { id: string; role: 'user' | 'assistant'; text: string; status: 'streaming' | 'completed' | 'cancelled' | 'failed'; attachments?: Attachment[]; operations?: Operation[] };
export type Conversation = { id: string; title: string; mode: 'chat' | 'assistant'; messages: Message[]; activeTurn: string | null; position: number; updatedAt?: string };
export type Header = Omit<Conversation, 'messages'>;
export type Summary = { id: string; title: string; mode: 'chat' | 'assistant'; activeTurn: string | null; updatedAt: string };
// An event carries the conversation's header and only the messages that update added or changed.
// `reset` means the client is too far behind to replay and should reload the snapshot.
export type Event = { seq: number; kind: 'reply.updated' | 'turn.completed' | 'turn.cancelled' | 'turn.failed' | 'input.accepted' | 'approval.required' | 'tool.updated' | 'reset'; conversation: Header; messages: Message[] };

/** The stream can't continue from this position; reload the snapshot. */
export class StreamReset extends Error {}

export async function request<T>(path: string, options: RequestInit = {}, base = '/api'): Promise<T> {
  const response = await fetch(`${base}${PREFIX}${path}`, {
    ...options, headers: { 'Content-Type': 'application/json', ...options.headers },
    redirect: 'error', signal: options.signal ?? AbortSignal.timeout(10000),
  });
  const data = await response.json().catch(() => null);
  if (!response.ok) throw new Error(`${data?.error || 'Request failed'} (${response.status})`);
  return data as T;
}

/** Applies one event to the conversation it follows. */
export function apply(conversation: Conversation, event: Event): Conversation {
  const messages = [...conversation.messages];
  for (const message of event.messages) {
    const index = messages.findIndex(m => m.id === message.id);
    if (index >= 0) messages[index] = message; else messages.push(message);
  }
  return { ...conversation, ...event.conversation, messages };
}

/**
 * Reads one event stream from `after`. Returns when the server ends the stream (it does so before
 * the hosting request limit); throws StreamReset when the client must reload the snapshot.
 */
export async function follow(id: string, after: number, signal: AbortSignal, receive: (event: Event) => void, base = '/api', opened?: () => void) {
  const response = await fetch(`${base}${PREFIX}/conversations/${encodeURIComponent(id)}/events?after=${after}`, { signal, redirect: 'error' });
  if (response.status === 400) throw new StreamReset('Event position is not valid here');
  if (!response.ok || !response.headers.get('content-type')?.startsWith('text/event-stream') || !response.body) throw new Error('Event stream unavailable');
  opened?.();
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = '';
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) return after;
      buffer += value.replace(/\r/g, '');
      let end: number;
      while ((end = buffer.indexOf('\n\n')) >= 0) {
        const frame = buffer.slice(0, end);
        buffer = buffer.slice(end + 2);
        const data = frame.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n');
        if (!data) continue;
        const event: Event = JSON.parse(data);
        if (event.conversation.id !== id) throw new Error('Cross-conversation event');
        if (event.kind === 'reset') throw new StreamReset('Too far behind to replay');
        if (!Number.isSafeInteger(event.seq) || event.seq <= after) continue;
        if (event.seq !== after + 1) throw new StreamReset('Event gap');
        after = event.seq;
        receive(event);
      }
    }
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

const pause = (ms: number, signal: AbortSignal) => new Promise<void>(done => {
  const timer = setTimeout(done, ms);
  signal.addEventListener('abort', () => { clearTimeout(timer); done(); }, { once: true });
});

/**
 * Keeps a conversation current until `signal` aborts: follows the stream, resumes when the server
 * ends it, reloads the snapshot after a reset or failure, and backs off while the service is unreachable.
 */
export async function stay(start: Conversation, signal: AbortSignal, update: (c: Conversation) => void, connected: (on: boolean) => void, base = '/api') {
  let current = start;
  let failures = 0;
  while (!signal.aborted) {
    try {
      await follow(current.id, current.position, signal, event => { current = apply(current, event); update(current); }, base, () => { failures = 0; connected(true); });
    } catch (error) {
      if (signal.aborted) return;
      if (!(error instanceof StreamReset)) {
        failures++;
        connected(false);
        await pause(Math.min(30000, 500 * 2 ** failures), signal);
      }
      try {
        current = await request<Conversation>(`/conversations/${current.id}`, { signal }, base);
        update(current);
      } catch { /* the next attempt retries */ }
    }
  }
}
