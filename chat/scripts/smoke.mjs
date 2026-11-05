// Browser journeys against an explicit offline fixture or an isolated real Go service through Next.
import { chromium } from 'playwright-core';
import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';

const target = new URL(process.env.CHAT_URL || process.env.CHAT_FIXTURE_URL || 'http://127.0.0.1:3230/chat/');
if (target.protocol !== 'http:' || target.hostname !== '127.0.0.1' || !['/', '/chat/', '/chat/index.html'].includes(target.pathname) || target.search || target.hash || target.username || target.password) throw new Error('Use an isolated loopback chat URL');
const response = await fetch(new URL('/api/v1/capabilities', target), { headers: { 'Sec-Fetch-Site': 'same-origin' }, redirect: 'error', signal: AbortSignal.timeout(15000) });
assert.equal(response.status, 200);
const capability = await response.json();
assert.equal(capability.contract, 'nori-chat-1');
assert.equal(capability.isolated, true, 'Browser smoke must not write to a production service');
assert.equal(capability.deterministic, true, 'Browser smoke requires the unpaid deterministic provider');
if (!process.env.CHAT_FIXTURE_URL) assert.equal(capability.fixture, false, 'Real browser verification cannot use an offline fixture');
const directory = process.env.CHAT_REPORT_DIR ? resolve(process.env.CHAT_REPORT_DIR) : resolve(import.meta.dirname, '../../.artifacts/migration/browser', new Date().toISOString().replaceAll(':', '-'));
await mkdir(directory, { recursive: true });
const browser = await chromium.launch({ headless: true, executablePath: process.env.CHAT_BROWSER_EXECUTABLE || undefined });
const errors = [], checks = [];
let passed = false;
const ownedTasks = [];
async function snapshot(page) {
  return page.evaluate(async () => {
    const id = localStorage.getItem('nori-chat-conversation');
    const result = await fetch(`/api/v1/conversations/${id}`);
    if (!result.ok) throw new Error(`Snapshot failed (${result.status})`);
    return result.json();
  });
}
async function until(read, accept, timeout = 15000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { const value = await read(); if (accept(value)) return value; await new Promise(resolve => setTimeout(resolve, 25)); }
  throw new Error('Browser state timed out');
}
try {
  for (const [name, viewport] of [['desktop', { width: 1440, height: 1000 }], ['phone', { width: 375, height: 812 }]]) {
    let assistant, background, task;
    const assistantSnapshot = () => assistant.evaluate(async () => (await fetch('/api/agent/v1/snapshot')).json());
    if (!capability.fixture) {
      assistant = await browser.newPage({ viewport });
      assistant.on('pageerror', error => errors.push(`${name} assistant: ${error.message}`));
      await assistant.goto(target.origin);
      const greeting = `hello ${name} ${crypto.randomUUID().slice(0, 8)}`;
      await assistant.getByLabel('Message Nori', { exact: true }).fill(greeting);
      await assistant.getByRole('button', { name: 'Send', exact: true }).click();
      await assistant.getByText(`Noted: ${greeting}`, { exact: true }).waitFor();
      await assistant.reload();
      await assistant.getByText(`Noted: ${greeting}`, { exact: true }).waitFor();
      background = `background ${name} ${crypto.randomUUID().slice(0, 8)}`;
      await assistant.getByLabel('Message Nori', { exact: true }).fill(background);
      await assistant.getByRole('button', { name: 'Send', exact: true }).click();
      const session = await until(assistantSnapshot, value => value.runs?.some(run => run.request?.includes(background) && run.status === 'running'));
      task = session.runs.find(run => run.request?.includes(background));
      ownedTasks.push(task.id);
      await assistant.getByRole('button', { name: 'Activity', exact: true }).click();
      await assistant.getByRole('article').filter({ hasText: background }).getByRole('button', { name: 'Stop task', exact: true }).waitFor();
      checks.push(`${name}: assistant send, render, reload and real background dispatch`);
    }
    const page = await browser.newPage({ viewport });
    page.on('pageerror', error => errors.push(`${name}: ${error.message}`));
    await page.goto(target.href);
    await page.getByText(capability.fixture ? 'Connected to offline fixture' : 'Connected to Nori', { exact: true }).waitFor();
    await page.getByRole('button', { name: '＋ New conversation' }).click();
    await page.getByRole('heading', { name: 'Start a conversation' }).waitFor();
    const first = `slow stream ${name} ${crypto.randomUUID().slice(0, 8)}`;
    await page.getByLabel('Message', { exact: true }).fill(first);
    await page.getByRole('button', { name: 'Send', exact: true }).click();
    await page.getByRole('button', { name: 'Stop reply' }).waitFor();
    const partial = await until(() => snapshot(page), c => c.messages.at(-1)?.text.length > 0 && c.activeTurn);
    await page.getByRole('button', { name: 'Stop reply' }).click();
    await page.getByText('Stopped', { exact: true }).waitFor();
    const stopped = await snapshot(page);
    assert.equal(stopped.messages.at(-1).status, 'cancelled');
    assert.ok(stopped.messages.at(-1).text.startsWith(partial.messages.at(-1).text));
    assert.ok(stopped.messages.at(-1).text.length > 0);
    checks.push(`${name}: chat partial stop preserves saved text`);
    await page.reload();
    await page.getByText('Stopped', { exact: true }).waitFor();
    assert.deepEqual(await snapshot(page), stopped);
    await page.getByRole('button', { name: '＋ New conversation' }).click();
    await page.getByRole('heading', { name: 'Start a conversation' }).waitFor();
    await page.getByLabel('Message', { exact: true }).fill(`Complete ${name} ${crypto.randomUUID().slice(0, 8)}`);
    await page.getByRole('button', { name: 'Send', exact: true }).click();
    await page.getByRole('button', { name: 'Stop reply' }).waitFor();
    await page.getByRole('button', { name: 'Disconnect', exact: true }).click();
    await page.getByRole('button', { name: 'Reconnect', exact: true }).waitFor();
    const final = await until(() => snapshot(page), c => !c.activeTurn);
    assert.equal(final.messages.at(-1).status, 'completed');
    assert.ok(final.messages.at(-1).text.length > 0);
    await page.getByRole('button', { name: 'Reconnect', exact: true }).click();
    await page.getByText(final.messages.at(-1).text, { exact: true }).waitFor();
    await page.reload();
    await page.getByText(final.messages.at(-1).text, { exact: true }).waitFor();
    assert.equal(await page.getByRole('article').count(), 2);
    assert.deepEqual(await snapshot(page), final);
    await page.screenshot({ path: resolve(directory, `chat-${name}.png`), fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    await page.locator(`nav button[data-conversation-id="${stopped.id}"]`).click();
    await page.getByText('Stopped', { exact: true }).waitFor();
    assert.equal(await page.getByRole('article').count(), 2);
    assert.deepEqual(await snapshot(page), stopped);
    checks.push(`${name}: chat send, render, disconnect, replay, reload, conversation isolation, layout`);
    if (!capability.fixture && name === 'desktop') {
      await page.getByRole('button', { name: '＋ New conversation' }).click();
      await page.getByRole('heading', { name: 'Start a conversation' }).waitFor();
      const submit = async text => {
        await page.getByLabel('Message', { exact: true }).fill(text);
        await page.getByRole('button', { name: 'Send', exact: true }).click();
      };
      const csv = `item,total\nparts,17\n`;
      await page.getByLabel('Attach CSV', { exact: true }).setInputFiles({ name: 'browser.csv', mimeType: 'text/csv', buffer: Buffer.from(csv) });
      await page.getByRole('list', { name: 'Attached files' }).getByText('browser.csv', { exact: false }).waitFor();
      await submit('Read the attached CSV');
      const read = await until(() => snapshot(page), c => !c.activeTurn && c.messages.some(m => m.operations?.some(op => op.name === 'upload_read' && op.outcome === 'completed')));
      const upload = read.messages.flatMap(m => m.operations ?? []).find(op => op.name === 'upload_read');
      assert.deepEqual(upload.result.rows.map(row => row.cells), [['item', 'total'], ['parts', '17']]);
      await page.getByText(read.messages.at(-1).text, { exact: true }).waitFor();
      await page.reload();
      await page.getByRole('link', { name: 'browser.csv', exact: true }).waitFor();
      await submit('Read my calendar');
      await until(() => snapshot(page), c => !c.activeTurn && c.messages.some(m => m.operations?.some(op => op.name === 'calendar_read' && op.outcome === 'completed')));
      const proposal = `Create a calendar event browser-${crypto.randomUUID().slice(0, 8)}`;
      for (const decision of ['decline', 'approve']) {
        await submit(proposal);
        const pending = await until(() => snapshot(page), c => c.messages.some(m => m.operations?.some(op => op.name === 'calendar_create' && op.outcome === 'prepared')));
        const operation = pending.messages.flatMap(m => m.operations ?? []).find(op => op.name === 'calendar_create' && op.outcome === 'prepared');
        const approval = page.getByRole('group', { name: 'Action approval' });
        await approval.getByText(operation.action.text, { exact: true }).waitFor();
        await page.screenshot({ path: resolve(directory, `approval-${decision}.png`), fullPage: true });
        await approval.getByRole('button', { name: decision === 'approve' ? 'Approve this action' : 'Decline', exact: true }).click();
        const expected = decision === 'approve' ? 'confirmed' : 'declined';
        await until(() => snapshot(page), c => !c.activeTurn && c.messages.some(m => m.operations?.some(op => op.id === operation.id && op.outcome === expected)));
      }
      checks.push('desktop: scoped CSV upload/read/reload, shared calendar read, exact action decline and approval');
    }
    if (assistant) {
      const running = await assistantSnapshot();
      assert.equal(running.runs.find(run => run.id === task.id)?.status, 'running', 'chat must not stop assistant background work');
      assert.ok(!JSON.stringify(running.messages).includes(first), 'chat messages must stay outside assistant history');
      assert.ok(!JSON.stringify(final).includes(background), 'assistant messages must stay outside chat history');
      await assistant.getByRole('article').filter({ hasText: background }).getByRole('button', { name: 'Stop task', exact: true }).click();
      await until(assistantSnapshot, value => value.runs?.find(run => run.id === task.id)?.status === 'cancelled', 30000);
      await assistant.reload();
      await assistant.getByRole('button', { name: 'Activity', exact: true }).click();
      await assistant.getByRole('article').filter({ hasText: background }).getByText('Stopped', { exact: true }).waitFor();
      await assistant.screenshot({ path: resolve(directory, `assistant-${name}.png`), fullPage: true });
      assert.equal(await assistant.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      const preserved = await page.evaluate(async id => (await fetch(`/api/v1/conversations/${id}`)).json(), stopped.id);
      assert.deepEqual(preserved, stopped, 'assistant cancellation must not mutate chat');
      checks.push(`${name}: assistant targeted task stop, reload, layout, and concurrent client isolation`);
      await assistant.close();
    }
    await page.close();
  }
  assert.deepEqual(errors, []);
  passed = true;
} finally {
  if (!passed) for (const run_id of ownedTasks) {
    await fetch(new URL('/api/agent/command', target), { method: 'POST', headers: { Origin: target.origin, 'Sec-Fetch-Site': 'same-origin', 'Content-Type': 'application/json' }, body: JSON.stringify({ id: crypto.randomUUID(), run_id, kind: 'stop' }), signal: AbortSignal.timeout(5000) }).catch(() => {});
  }
  if (!passed) for (const [index, page] of browser.contexts().flatMap(c => c.pages()).entries()) {
    await page.screenshot({ path: resolve(directory, `failure-${index}.png`), fullPage: true }).catch(() => {});
    await writeFile(resolve(directory, `failure-${index}.txt`), await page.locator('body').innerText().catch(() => 'Page unavailable'));
  }
  await writeFile(resolve(directory, 'report.json'), JSON.stringify({ passed, fixture: capability.fixture, scope: capability.fixture ? 'Explicit offline fixture' : 'Real isolated Go service via authenticated Next proxy', target: target.href, capability, errors, checks }, null, 2));
  await browser.close();
  console.log(`Browser evidence: ${directory}`);
}
