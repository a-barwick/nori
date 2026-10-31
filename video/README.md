# Nori film

A 48-second Remotion film for the Nova pitch: `out/nori.mp4`, 1920×1080, 30 fps, cut at 120 BPM (one beat is 15 frames). Scene timing, captions and camera moves live in `src/timeline.json`; the score reads the same file.

```sh
npm install
npx playwright-core install chrome   # only if system Chrome is missing
(cd ../web && bun install --frozen-lockfile && ./node_modules/.bin/next dev -H 127.0.0.1 --port 3211)
npm run shots     # real screenshots of investor.docusign.com
npm run capture   # films the real frontend into public/clips
npm run score     # synthesizes public/audio/score.wav
npm run dev       # preview in Remotion Studio
npm run render
```

`capture/capture.mjs` answers `/api/*` in the browser with a scripted session, so it never touches the runtime. The interface is real; the conversation is scripted. On-screen figures come from [Docusign's Q2 FY27 earnings release](https://s21.q4cdn.com/706790701/files/doc_earnings/2027/q2/earnings-result/2Q27-Earnings-Release.pdf) and the [October 13 Salesforce release](https://investor.docusign.com/news-and-events/press-releases/news-details/2026/Docusign-and-Salesforce-Launch-Intelligent-Agreement-Management-Natively-Across-Salesforce-to-Power-Agentic-Agreement-Workflows/default.aspx). The film's on-screen label says the demo is scripted; remove it from `src/scenes.tsx` only if the footage is replaced with a genuine run.
