import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { createFixture } from '../migration/fixture.ts';
import { PREFIX } from '../migration/contract.ts';

export default defineConfig({
  envDir: false,
  plugins: [svelte(), {
    name: 'local-chat-fixture',
    configureServer(server) {
      const fixture = createFixture();
      server.httpServer?.once('close', () => fixture.close());
      server.middlewares.use(async (req, res, next) => {
        if (!req.url?.startsWith(PREFIX + '/')) return next();
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
          const request = new Request(`http://${req.headers.host}${req.url}`, {
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
  }],
});
