// Browser journey for the preparation fixture only. Uses an existing Chromium installation.
import { chromium } from 'playwright-core';
import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';

const target = new URL(process.env.CHAT_FIXTURE_URL || 'http://127.0.0.1:3230');
if (target.protocol !== 'http:' || target.hostname !== '127.0.0.1' || target.pathname !== '/' || target.search || target.hash || target.username || target.password) throw new Error('Use a loopback fixture origin');
const capability = await fetch(new URL('/__migration/v1/capabilities', target), { redirect: 'error', signal: AbortSignal.timeout(5000) });
assert.equal((await capability.json()).mode, 'fixture', 'Browser smoke only writes to the fixture');
const directory = resolve(import.meta.dirname, '../../.artifacts/migration/browser', new Date().toISOString().replaceAll(':', '-'));
await mkdir(directory, { recursive: true });
const browser = await chromium.launch({ headless: true, executablePath: process.env.CHAT_BROWSER_EXECUTABLE || undefined });
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
const errors = [];
page.on('pageerror', error => errors.push(error.message));
let passed = false;
try {
  await page.goto(target.href);
  await page.getByText('Connected to local fixture', { exact: false }).waitFor();
  await page.getByRole('button', { name: '＋ New conversation' }).click();
  await page.getByRole('heading', { name: 'Try a conversation' }).waitFor();
  const first = `First check ${crypto.randomUUID().slice(0, 8)}`;
  await page.getByLabel('Message', { exact: true }).fill(first);
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await page.getByRole('button', { name: 'Stop reply' }).waitFor();
  await page.waitForFunction(() => [...document.querySelectorAll('article p')].some(p => p.textContent.startsWith('Fixture')));
  await page.getByRole('button', { name: 'Stop reply' }).click();
  await page.getByText('Stopped', { exact: true }).waitFor();
  await page.getByRole('button', { name: '＋ New conversation' }).click();
  await page.getByRole('heading', { name: 'Try a conversation' }).waitFor();
  await page.getByLabel('Message', { exact: true }).fill('Check a second independent conversation');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await page.getByRole('button', { name: 'Stop reply' }).waitFor();
  await page.getByRole('button', { name: 'Disconnect', exact: true }).click();
  await page.getByRole('button', { name: 'Reconnect', exact: true }).waitFor();
  await page.waitForTimeout(2500);
  await page.getByRole('button', { name: 'Reconnect', exact: true }).click();
  await page.getByText('No model or external tool was called.', { exact: false }).waitFor();
  await page.reload();
  await page.getByText('No model or external tool was called.', { exact: false }).waitFor();
  assert.equal(await page.getByRole('article').count(), 2);
  await page.screenshot({ path: resolve(directory, 'desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 375, height: 812 });
  await page.screenshot({ path: resolve(directory, 'phone.png'), fullPage: true });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.getByRole('button', { name: first, exact: true }).click();
  await page.getByText('Stopped', { exact: true }).waitFor();
  assert.equal(await page.getByRole('article').count(), 2);
  assert.deepEqual(errors, []);
  passed = true;
} finally {
  await writeFile(resolve(directory, 'report.json'), JSON.stringify({ passed, scope: 'Local fixture only', errors, checks: ['send', 'partial stop', 'separate conversations', 'disconnect', 'reconnect', 'reload', '375px layout', 'no browser errors'] }, null, 2));
  await browser.close();
  console.log(`Browser evidence: ${directory}`);
}
