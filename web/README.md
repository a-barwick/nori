# Nori web

Desktop web interface for Nori, built from the Nori design spec. Next.js 16 (App Router, Turbopack), Tailwind v4 on semantic tokens, Radix primitives, Zustand.

This is demo work: the thread, agent panel, Ideas, Goals and Library run on seed data, and anything you type gets a live reply from Claude through `src/app/api/nori/route.ts`. Later, `src/lib/nori/backend.ts` is the seam where the core agent service API plugs in.

## Run

```bash
bun install
bun run dev
```

Then open http://127.0.0.1:3000 (pass `--port` to pick another). The dev and start scripts bind to 127.0.0.1 only, and `/api/nori` rejects non-local requests, because the route spends your API key.

## Environment

Keys live in `web/.env` (gitignored). The app reads one:

| Key | Used for |
| --- | --- |
| `ANTHROPIC_API_KEY` | Live replies (Claude Sonnet 5.5, medium effort) |

Without it, scripted moments still work and typed messages show a neutral error with Retry.

## Where things are

- `src/app/globals.css`: color, type, radius, shadow and motion tokens (spec §4). Components use semantic names only.
- `src/lib/hybrid.ts`: the bubble vs page rule (spec §5.2), decided from the markdown's structure.
- `src/lib/units.ts`: grouping, pinched corners, time separators (spec §5.1, §5.6).
- `src/lib/seed.ts`: seed content in Nori's voice. Settings → Reset demo data restores it.
- `src/lib/nori/engine.ts`: scripted moments (chips, approvals, Ideas, Goals) and live replies.
- `src/components/mark/NoriMark.tsx`: the mark and its states (idle, attentive, thinking, writing, listening, pleased).

## Requirements

- [Avatar state protocol](docs/avatar.md): modes, gestures and gaze for Nori's mark, and the claim rules that replace the single `markState`. Not built yet.
