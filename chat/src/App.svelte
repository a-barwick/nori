<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { request, stay, type Conversation, type Capabilities, type Attachment, type Summary, CONTRACT } from './contract';

  let messagesElement: HTMLElement;
  let shownConversation: string | undefined;
  let capabilities = $state<Capabilities | null>(null);
  let chats = $state<Summary[]>([]);
  let current = $state<Conversation | null>(null);
  let draft = $state('');
  let error = $state('');
  let connected = $state(true);
  let sending = $state(false);
  let uploading = $state(false);
  let files = $state<Attachment[]>([]);
  let deciding = $state('');
  let stream: AbortController | null = null;
  let generation = 0;
  let retry: { id: string; message: string; conversation: string; attachments: string[] } | null = null;
  const waiting = $derived(!!current?.activeTurn && current.messages.at(-1)?.operations?.at(-1)?.outcome === 'prepared');
  $effect.pre(() => {
    const id = current?.id;
    const position = current?.position;
    if (!messagesElement || position === undefined) return;
    const followBottom = id !== shownConversation || messagesElement.scrollHeight - messagesElement.scrollTop - messagesElement.clientHeight < 100;
    shownConversation = id;
    if (followBottom) void tick().then(() => messagesElement?.scrollTo({ top: messagesElement.scrollHeight }));
  });
  const list = async () => {
    const result = await request<{ conversations: Summary[] }>('/conversations');
    chats = result.conversations.filter(c => c.mode === 'chat');
  };
  // Keeps the sidebar row in step with the open conversation.
  function remember(c: Conversation) {
    const row: Summary = { id: c.id, title: c.title, mode: c.mode, activeTurn: c.activeTurn, updatedAt: c.updatedAt ?? new Date().toISOString() };
    chats = [row, ...chats.filter(chat => chat.id !== c.id)];
  }
  function show(c: Conversation) {
    if (current?.id === c.id && c.position < current.position) return;
    current = c;
    if (chats[0]?.id !== c.id || chats[0].title !== c.title || chats[0].activeTurn !== c.activeTurn) remember(c);
  }
  function disconnect() { generation++; stream?.abort(); stream = null; }
  async function select(id: string) {
    if (current?.id !== id) files = [];
    disconnect();
    const selected = generation;
    error = '';
    try {
      const snapshot = await request<Conversation>(`/conversations/${id}`);
      if (selected !== generation) return;
      current = snapshot;
      localStorage.setItem('nori-chat-conversation', id);
      stream = new AbortController();
      connected = true;
      void stay(snapshot, stream.signal, c => { if (selected === generation) show(c); }, on => { if (selected === generation) connected = on; });
    } catch (e) { if (selected === generation) error = String(e); }
  }
  // A new conversation is created when its first message is sent, so opening the page never leaves empty chats.
  function startNew() {
    disconnect();
    current = null; files = []; error = ''; retry = null;
    localStorage.removeItem('nori-chat-conversation');
  }
  async function send(event: SubmitEvent) {
    event.preventDefault();
    if (current?.activeTurn || sending || uploading || !draft.trim()) return;
    sending = true; error = '';
    try {
      if (!current) {
        const created = await request<Conversation>('/conversations', { method: 'POST', body: '{}' });
        await select(created.id);
        // select() replaced `current`; read it again rather than the narrowed value.
        if ((current as Conversation | null)?.id !== created.id) return;
      }
      const id = current!.id;
      const message = draft.trim();
      const attachments = files.map(file => file.id);
      if (!retry || retry.message !== message || retry.conversation !== id || JSON.stringify(retry.attachments) !== JSON.stringify(attachments)) retry = { id: crypto.randomUUID(), message, conversation: id, attachments };
      const saved = await request<{ conversation: Conversation }>(`/conversations/${id}/messages`, { method: 'POST', body: JSON.stringify({ id: retry.id, text: message, ...(attachments.length ? { attachments } : {}) }) });
      retry = null; draft = ''; files = [];
      if (current?.id === id) show(saved.conversation);
    } catch (e) { error = `${String(e)}. Send again to retry the same message.`; }
    finally { sending = false; }
  }
  async function stop() {
    if (!current?.activeTurn) return;
    const id = current.id;
    try { show(await request<Conversation>(`/conversations/${id}/cancel`, { method: 'POST', body: JSON.stringify({ turnId: current.activeTurn }) })); }
    catch (e) { error = String(e); }
  }
  async function remove() {
    if (!current || current.activeTurn) return;
    if (!confirm(`Delete “${current.title}” and its files? This can't be undone.`)) return;
    const id = current.id;
    try {
      await request(`/conversations/${id}`, { method: 'DELETE' });
      chats = chats.filter(chat => chat.id !== id);
      if (chats[0]) await select(chats[0].id); else startNew();
    } catch (e) { error = String(e); }
  }
  async function attach(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0]; input.value = '';
    if (!file) return;
    if (file.size > 10 * 1024 * 1024) { error = 'A file can be at most 10 MB.'; return; }
    uploading = true; error = '';
    try {
      if (!current) {
        const created = await request<Conversation>('/conversations', { method: 'POST', body: '{}' });
        await select(created.id);
      }
      const id = current!.id;
      const query = new URLSearchParams({ id: crypto.randomUUID(), filename: file.name });
      const response = await fetch(`/api/v1/conversations/${id}/uploads?${query}`, { method: 'POST', headers: { 'Content-Type': file.type || 'text/csv' }, body: file, redirect: 'error', signal: AbortSignal.timeout(30000) });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || `Upload failed (${response.status})`);
      if (current?.id === id) files = [...files, result];
    } catch (e) { error = String(e); }
    finally { uploading = false; }
  }
  async function decide(questionId: string, decision: 'approve' | 'decline') {
    if (!current || deciding) return;
    const id = current.id; deciding = questionId; error = '';
    try {
      const saved = await request<Conversation>(`/conversations/${id}/approvals`, { method: 'POST', body: JSON.stringify({ questionId, decision }) });
      if (current?.id === id) show(saved);
    } catch (e) { error = String(e); }
    finally { deciding = ''; }
  }
  onMount(() => {
    void (async () => {
      try {
        capabilities = await request<Capabilities>('/capabilities');
        if (capabilities.contract !== CONTRACT) throw new Error('The service uses an incompatible chat contract.');
        await list();
        const saved = localStorage.getItem('nori-chat-conversation');
        const id = chats.find(c => c.id === saved)?.id ?? chats[0]?.id;
        if (id) await select(id);
      } catch (e) { error = String(e); }
    })();
    return disconnect;
  });
</script>

<svelte:head><meta name="description" content="Independent conversations with Nori"></svelte:head>

<div class="shell">
  <aside>
    <div class="brand">Nori <span>Chat</span></div>
    <button class="new" onclick={startNew} disabled={sending}>＋ New conversation</button>
    <nav aria-label="Conversations">
      {#each chats as chat}
        <button data-conversation-id={chat.id} class:active={current?.id === chat.id} onclick={() => select(chat.id)} disabled={sending}>{chat.title}</button>
      {/each}
    </nav>
    <div class="aside-note"><a href="/">Open assistant</a><p>{capabilities?.fixture ? "Offline fixture · scripted replies" : "Independent conversations"}</p></div>
  </aside>
  <main>
    <header>
      <div><h1>{current?.title ?? 'New conversation'}</h1><p>{capabilities?.fixture ? "Offline fixture · resets when the server restarts" : "Saved by Nori"}</p></div>
      {#if current}<button onclick={remove} disabled={!!current.activeTurn}>Delete chat</button>{/if}
    </header>
    <section bind:this={messagesElement} class="messages" aria-label="Messages" aria-live="polite" aria-busy={!!current?.activeTurn}>
      {#if !current?.messages?.length}
        <div class="empty"><div class="mark">n</div><h2>Start a conversation</h2><p>{capabilities?.fixture ? "Responses are scripted for offline testing." : "What would you like to talk about?"}</p></div>
      {/if}
      {#each current?.messages ?? [] as message (message.id)}
        <article class:user={message.role === 'user'}>
          <div class="role">{message.role === 'user' ? 'You' : 'Nori'}</div>
          {#if !message.operations?.some(operation => operation.outcome === 'prepared' && operation.action?.text === message.text)}<p>{message.text || (message.operations?.length ? '' : 'Starting reply…')}</p>{/if}
          {#if message.attachments?.length}<ul aria-label="Message attachments">{#each message.attachments as file}<li><a href={`/api/v1/conversations/${current?.id}/uploads/${file.id}`} target="_blank" rel="noreferrer">{file.name}</a></li>{/each}</ul>{/if}
          {#each message.operations ?? [] as operation (operation.id)}
            {#if operation.outcome === 'prepared' && operation.action}
              <div class="approval" role="group" aria-label="Action approval">
                <p>{operation.action.text}</p>
                <button onclick={() => decide(operation.action!.question_id, 'approve')} disabled={!!deciding}>Approve this action</button>
                <button onclick={() => decide(operation.action!.question_id, 'decline')} disabled={!!deciding}>Decline</button>
              </div>
            {:else if operation.action}
              <details class="action"><summary>{operation.name}: {operation.outcome}</summary><p>{operation.action.text}</p></details>
            {:else}<small>{operation.name}: {operation.outcome}</small>{/if}
          {/each}
          {#if message.status !== 'completed'}<small>{message.status === 'cancelled' ? 'Stopped' : message.status === 'failed' ? 'Reply interrupted' : waiting && message === current?.messages.at(-1) ? 'Waiting for your approval' : 'Responding…'}</small>{/if}
        </article>
      {/each}
    </section>
    <footer>
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      {#if files.length}<ul class="files" aria-label="Attached files">{#each files as file}<li>{file.name} <button aria-label={`Remove ${file.name}`} onclick={() => files = files.filter(f => f.id !== file.id)}>Remove</button></li>{/each}</ul>{/if}
      {#if !capabilities?.fixture}<label class="attach">{uploading ? 'Uploading…' : 'Attach CSV'}<input aria-label="Attach CSV" type="file" accept=".csv,text/csv" onchange={attach} disabled={uploading || sending || !!current?.activeTurn} /></label>{/if}
      <form onsubmit={send}>
        <label class="sr-only" for="message">Message</label>
        <textarea id="message" bind:value={draft} maxlength="4000" placeholder="Write a message…" rows="2" disabled={sending}></textarea>
        {#if current?.activeTurn && !waiting}<button type="button" onclick={stop}>Stop reply</button>{:else}<button class="send" type="submit" disabled={!draft.trim() || sending || uploading || waiting}>Send</button>{/if}
      </form>
      <p class="footnote">{waiting ? 'Approve or decline the action above to continue' : connected ? (capabilities?.fixture ? 'Connected to offline fixture' : 'Connected to Nori') : 'Reconnecting…'}</p>
    </footer>
  </main>
</div>

<style>
  :global(*){box-sizing:border-box} :global(body){margin:0;background:#f8f7f4;color:#242a27;font:15px/1.5 system-ui,sans-serif} :global(button),:global(textarea){font:inherit} :global(button){cursor:pointer} :global(button:disabled){cursor:default;opacity:.45} :global(button:focus-visible),:global(textarea:focus-visible){outline:2px solid #4e7762;outline-offset:3px}
  .action{font-size:12px;color:#646c62;margin:6px 0}.action p{white-space:pre-wrap;margin:6px 0 0}.attach{display:block;font-size:12px;margin:0 0 8px}.attach input{font-size:12px;margin-left:8px;max-width:240px}.approval{border:1px solid #a8b7a1;padding:12px;border-radius:8px;margin:8px 0}.approval button{margin-right:8px}.files{font-size:12px;padding-left:18px}.files button{font-size:11px}a{color:inherit}
  .shell{display:grid;grid-template-columns:250px minmax(0,1fr);height:100dvh;min-height:100dvh} aside{min-height:0;background:#eeede7;border-right:1px solid #ddddd4;padding:28px 18px;display:flex;flex-direction:column;gap:24px}.brand{font-size:24px;font-weight:650;padding:0 10px}.brand span{font-weight:400;color:#656d65;margin-left:5px}.new,header button{background:transparent;border:1px solid #c9cec5;border-radius:8px;padding:9px 12px}nav{flex:1;min-height:0;overflow:auto;display:flex;flex-direction:column;gap:5px}nav button{flex-shrink:0;border:0;background:transparent;text-align:left;border-radius:7px;padding:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}nav button.active{background:#dfe5dc}.aside-note{margin-top:auto;font-size:12px;color:#646c62;padding:10px}main{display:flex;flex-direction:column;min-width:0;max-height:100dvh}header{padding:23px 32px;border-bottom:1px solid #e0e2da;display:flex;gap:16px;justify-content:space-between;align-items:center}h1{font-size:16px;margin:0;font-weight:600;overflow-wrap:anywhere}header p{font-size:12px;color:#6b7169;margin:4px 0 0}header button{font-size:12px;flex-shrink:0}.messages{flex:1;overflow:auto;padding:30px max(24px,calc((100% - 760px)/2));min-height:200px}.empty{margin:8vh auto;text-align:center;color:#626d62}.mark{font-family:Georgia,serif;font-style:italic;font-size:48px;color:#456a50}.empty h2{font-size:24px;color:#303e31;font-weight:500;margin:14px 0}.empty p{font-size:14px}article{margin-bottom:25px;overflow-wrap:anywhere}article p{white-space:pre-wrap;margin:7px 0}.role{font-size:12px;font-weight:650;color:#657160}.user{background:#e9eee6;padding:16px 20px;border-radius:12px;margin-left:12%}small{color:#70766e}footer{padding:15px max(24px,calc((100% - 760px)/2)) 20px}form{display:flex;gap:12px;align-items:flex-end;border:1px solid #cbd3c7;background:#fff;border-radius:12px;padding:12px}textarea{flex:1;min-width:0;resize:vertical;max-height:180px;border:0;background:transparent;color:inherit}form button{border:0;border-radius:7px;padding:9px 15px;background:#e4e9df}.send{background:#365640;color:#fff}.footnote{font-size:11px;color:#70766e;text-align:center;margin:9px 0 0}.error{color:#963928;font-size:13px}.sr-only{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%)}
  @media(max-width:640px){.shell{height:auto;grid-template-columns:1fr}aside{padding:14px;gap:12px;border-right:0;border-bottom:1px solid #ddd}.brand{font-size:20px}nav{flex:none;max-height:105px;overflow:auto}.aside-note{display:none}main{max-height:none;min-height:65dvh}header{padding:16px;align-items:flex-start}.messages{padding:20px 16px;max-height:55dvh}footer{padding:12px 16px}.empty{margin:25px auto}.user{margin-left:5%}}
</style>
