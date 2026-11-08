import type { Attachment } from "../types";

export const CONTRACT = 'nori-chat-2';
export const chatAPI = (path: string) => `/api/v1${path}`;
export const conversationPath = (id: string) => `/conversations/${encodeURIComponent(id)}`;
export type Capabilities = { contract: string; service: string; persistence?: string; provider?: string; model?: string; conversationModes: string[] };
export type Operation = { id: string; call_id: string; name: string; effect_class: string; outcome: string; action?: { digest: string; text: string; spec: Record<string, string> }; result?: Record<string, unknown> };
export type Message = { id: string; sequence: number; role: 'user' | 'assistant'; text: string; status: 'streaming' | 'completed' | 'cancelled' | 'failed'; attachments?: Attachment[]; operations?: Operation[] };
export type Conversation = { id: string; title: string; mode: 'chat' | 'assistant'; messages: Message[]; activeTurn: string | null; position: number; nextBefore?: number; updatedAt?: string };
type Header = Omit<Conversation, 'messages' | 'nextBefore'>;
export type Summary = { id: string; title: string; mode: 'chat' | 'assistant'; activeTurn: string | null; updatedAt: string };
type Event = { seq: number; kind: 'reply.updated' | 'turn.completed' | 'turn.cancelled' | 'turn.failed' | 'input.accepted' | 'tool.updated' | 'reset'; conversation: Header; messages: Message[] };

export type MessagePage = { messages: Message[]; nextBefore?: number };

type Recovery = "snapshot" | "rewind";

export async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const timeout = AbortSignal.timeout(10_000);
  const response = await fetch(chatAPI(path), {
    ...options, headers: { 'Content-Type': 'application/json', ...options.headers }, redirect: 'error',
    signal: options.signal ? AbortSignal.any([options.signal, timeout]) : timeout,
  });
  const data = await response.json().catch(() => null);
  if (!response.ok) throw new Error(`${data?.error || 'Request failed'} (${response.status})`);
  return data as T;
}

export function mergeMessages(older: Message[], newer: Message[]): Message[] {
  const messages = new Map(older.map((message) => [message.id, message]));
  for (const message of newer) messages.set(message.id, message);
  return [...messages.values()].sort((a, b) => a.sequence - b.sequence);
}

async function follow(id: string, after: number, signal: AbortSignal, receive: (event: Event) => void, opened: () => void): Promise<Recovery | undefined> {
  const response = await fetch(chatAPI(`${conversationPath(id)}/events?after=${after}`), { signal, redirect: 'error' });
  if (response.status === 400) return "rewind";
  if (!response.ok || !response.headers.get('content-type')?.startsWith('text/event-stream') || !response.body) throw new Error('Event stream unavailable');
  opened();
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = '';
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) return;
      buffer += value.replace(/\r/g, '');
      let end: number;
      while ((end = buffer.indexOf('\n\n')) >= 0) {
        const frame = buffer.slice(0, end);
        buffer = buffer.slice(end + 2);
        const data = frame.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n');
        if (!data) continue;
        const event: Event = JSON.parse(data);
        if (event.conversation.id !== id) throw new Error('Cross-conversation event');
        if (event.kind === 'reset') return "snapshot";
        if (!Number.isSafeInteger(event.seq) || event.seq <= after) continue;
        if (event.seq !== after + 1) return "snapshot";
        after = event.seq;
        receive(event);
      }
    }
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

function pause(ms: number, signal: AbortSignal) {
  if (signal.aborted) return;
  return new Promise<void>((resolve) => {
    const timer = setTimeout(done, ms);
    signal.addEventListener('abort', done);
    function done() {
      clearTimeout(timer);
      signal.removeEventListener('abort', done);
      resolve();
    }
  });
}

type StreamCallbacks = {
  update: (header: Header, messages: Message[]) => void;
  recover: (snapshot: Conversation, recovery: Recovery) => void;
  connected: (on: boolean) => void;
};

export async function stay(start: Conversation, signal: AbortSignal, { update, recover, connected }: StreamCallbacks) {
  const id = start.id;
  let after = start.position;
  let failures = 0;
  let recovery: Recovery | undefined;
  const backoff = () => {
    connected(false);
    return pause(Math.min(30_000, 500 * 2 ** ++failures), signal);
  };
  while (!signal.aborted) {
    if (!recovery) {
      try {
        recovery = await follow(id, after, signal, (event) => {
          after = event.seq;
          update(event.conversation, event.messages);
        }, () => { failures = 0; connected(true); });
        // A clean end before the hosting deadline resumes from the last event.
        if (!recovery) continue;
      } catch {
        if (signal.aborted) return;
        recovery = "snapshot";
        await backoff();
      }
    }
    if (signal.aborted) return;
    try {
      const snapshot = await request<Conversation>(conversationPath(id), { signal });
      if (snapshot.id !== id) throw new Error("The service returned a different conversation.");
      recover(snapshot, snapshot.position < after ? "rewind" : recovery);
      after = snapshot.position;
      recovery = undefined;
    } catch {
      if (signal.aborted) return;
      await backoff();
    }
  }
}
