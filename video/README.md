# Nori film

A 48-second Remotion film for the Nova pitch: `out/nori.mp4`, 1920×1080, 30 fps, cut at 120 BPM (one beat is 15 frames). Scene timing, captions and camera moves live in `src/timeline.json`; the score reads the same file.

```sh
npm install
npx playwright-core install chrome   # only if system Chrome is missing
(cd ../web && bun install --frozen-lockfile && ./node_modules/.bin/next dev -H 127.0.0.1 --port 3211)
npm run capture   # films the real frontend into public/clips
npm run score     # synthesizes public/audio/score.wav
npm run dev       # preview in Remotion Studio
npm run render
```

`capture/run.json` holds session snapshots polled from the live runtime during a real Nori run on 2026-10-31: every message, task event and result in the film came from Opus 5.5, `gemini-3.8-flash` and the virtual computer, with the task screenshot in `public/real`. `capture/capture.mjs` replays those snapshots through the real frontend at speed; the typing is re-performed from the real message text. The trust cards, stat and closing line come from the pitch deck and are not product footage.
