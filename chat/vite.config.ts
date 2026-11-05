import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { createFixture } from './fixture.ts';
import { PREFIX } from './src/contract.ts';

// The fixture is an explicit offline mode. Normal development reaches the authenticated Next server.
const offline = process.env.NORI_CHAT_FIXTURE === '1';
const web = process.env.NORI_CHAT_WEB_URL || 'http://127.0.0.1:3000';
const target = new URL(web);
if (target.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(target.hostname) || target.pathname !== '/' || target.search || target.hash || target.username || target.password) throw new Error('NORI_CHAT_WEB_URL must be a loopback HTTP origin');

export default defineConfig({
  envDir: false,
  base: '/chat/',
  build: { outDir: '../web/public/chat', emptyOutDir: true },
  server: { proxy: offline ? undefined : { '/api/v1': { target: web } } },
  plugins: [svelte(), ...(offline ? [{
    name: 'offline-chat-fixture',
    configureServer(server: import('vite').ViteDevServer) {
      const fixture = createFixture();
      server.httpServer?.once('close', () => fixture.close());
      server.middlewares.use(async (req, res, next) => {
        if (!req.url?.startsWith('/api' + PREFIX + '/')) return next();
        const controller = new AbortController();
        res.once('close', () => controller.abort());
        try {
          const chunks: Buffer[] = [];
          let size = 0;
          for await (const chunk of req) {
            size += chunk.length;
            if (size > 16384) { res.writeHead(413); res.end(); return; }
            chunks.push(Buffer.from(chunk));
          }
          const headers = new Headers();
          for (const [key, value] of Object.entries(req.headers)) if (typeof value === 'string') headers.set(key, value);
          const request = new Request(`http://${req.headers.host}${req.url.slice(4)}`, {
            method: req.method, headers, signal: controller.signal,
            body: chunks.length ? Buffer.concat(chunks) : undefined,
          });
          const response = await fixture.handle(request);
          res.writeHead(response.status, Object.fromEntries(response.headers));
          res.flushHeaders();
          if (response.body) {
            const reader = response.body.getReader();
            try {
              while (!controller.signal.aborted) {
                const { done, value } = await reader.read();
                if (done) break;
                res.write(value);
              }
            } finally { await reader.cancel(); }
          }
          res.end();
        } catch { if (!res.headersSent) res.writeHead(500); res.end(); }
      });
    },
  }] : [])],
});
