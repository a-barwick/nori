// Shared HTTP contract implemented by the Go service and both web clients.
export const CONTRACT = 'nori-chat-1';
export const PREFIX = '/v1';
export type Attachment = { id: string; name: string; media_type: string; size: number };
export type Capabilities = { contract: string; service: string; fixture: boolean; isolated: boolean; deterministic?: boolean; persistence?: string; provider?: string; model?: string; conversationModes: string[] };
export type Message = { id: string; role: 'user' | 'assistant'; text: string; status: 'streaming' | 'completed' | 'cancelled' | 'failed'; attachments?: Attachment[] };
export type Conversation = { id: string; title: string; mode: 'chat' | 'assistant'; messages: Message[]; activeTurn: string | null; position: number };
export type Event = { seq: number; kind: 'reply.updated' | 'turn.completed' | 'turn.cancelled' | 'turn.failed' | 'input.accepted' | 'approval.required' | 'tool.updated'; conversation: Conversation };

export async function request<T>(path: string, options: RequestInit = {}, base = '/api'): Promise<T> {
  const response = await fetch(`${base}${PREFIX}${path}`, {
    ...options, headers: { 'Content-Type': 'application/json', ...options.headers },
    redirect: 'error', signal: options.signal ?? AbortSignal.timeout(10000),
  });
  const data = await response.json().catch(() => null);
  if (!response.ok) throw new Error(`${data?.error || 'Request failed'} (${response.status})`);
  return data as T;
}

// Full-text updates, not append-only deltas: replay cannot append the same token twice.
export async function follow(id: string, after: number, signal: AbortSignal, receive: (event: Event) => void, base = '/api') {
  const response = await fetch(`${base}${PREFIX}/conversations/${encodeURIComponent(id)}/events?after=${after}`, { signal, redirect: 'error' });
  if (!response.ok || !response.headers.get('content-type')?.startsWith('text/event-stream') || !response.body) throw new Error('Event stream unavailable');
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = '';
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += value.replace(/\r/g, '');
      let end: number;
      while ((end = buffer.indexOf('\n\n')) >= 0) {
        const frame = buffer.slice(0, end);
        buffer = buffer.slice(end + 2);
        const data = frame.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n');
        if (!data) continue;
        const event: Event = JSON.parse(data);
        if (event.conversation.id !== id) throw new Error('Cross-conversation event');
        if (!Number.isSafeInteger(event.seq) || event.seq <= after) continue;
        if (event.seq !== after + 1) throw new Error('Event gap; reload snapshot');
        after = event.seq;
        receive(event);
      }
    }
    if (!signal.aborted) throw new Error('Event stream ended; reconnect');
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}
