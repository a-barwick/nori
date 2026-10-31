# Nori film

Everything needed to change or reshoot the Nori film. Austin approved the look, theme and animation of the 2026-10-31 film and wants them as the quality bar: tweak rather than starting over.

## Start here

`SCRIPT.md` is the approved script for the current film, v5 (about 1:31). It is a hype film: scenes 3 to 7 are scripted, with fictional people and data, played through the real frontend. Most changes only need a re-render:

| Change | What to do | Live spend |
| --- | --- | --- |
| Captions, camera, timing, deck scenes, music | [Edit and render](#edit-and-render) | None |
| Messages, tasks, or what happens in the interface | [Refilm](#refilm), about two minutes | None |

The video files stay out of Git. The v5 take is at `~/Documents/dev/agents/nori-film-media/2026-10-31-v5/`, v4 at `.../2026-10-31-v4/`, v3 at `.../2026-10-31-v3/`, the earlier approved film at `~/Documents/dev/agents/nori-film-media/2026-10-31/`, and worktree cleanup can't touch either. The Git tag `film-2026-10-31` holds that film's code and its real run data. The tags `film-v4` and `film-v5` hold the code for those takes.

## Setup

```bash
cd demo && npm ci
```

```bash
npx skills experimental_install
```

The second command restores the pinned Remotion skills from `skills-lock.json`. If it fails, use `npx skills add remotion-dev/skills`. Read `.agents/skills/remotion-best-practices` before writing Remotion code.

Restore the footage:

```bash
M=~/Documents/dev/agents/nori-film-media/2026-10-31-v5; mkdir -p public/clips out; cp $M/desktop.mp4 $M/phone.mp4 $M/markers.json public/clips/
```

## Edit and render

```bash
npm run score
```

```bash
npm run dev
```

```bash
npm run render
```

`npm run dev` opens Remotion Studio. `npm run render` writes `out/nori.mp4` at CRF 18. Run `npm run score` after any change to the score.

| File | Holds |
| --- | --- |
| `SCRIPT.md` | The approved script: every frame, line and cue |
| `src/timeline.json` | Scene lengths |
| `src/opening.tsx` | The opening card, scenes 1 and 2: the chores, the walls, the rice and the mark |
| `src/scenes.tsx` | Scenes 3 to 8: camera, captions, the phone's moves, the lock screen, the renewal tracker and End |
| `src/kit.tsx` | Colours, Figtree, the pop animation, sticker captions, the real Nori mark, the deck's ribbons |
| `capture/write-run.mjs` | The scripted messages and tasks; writes `capture/run.json` |
| `capture/capture.mjs` | Plays `run.json` through the real frontend, one film frame at a time |
| `capture/score.mjs` | The synthesized music and effects, on the script's frame cues |
| `sources/` | The pitch deck and the Docusign Q2 release |

Keep the scene lengths on the beat. One beat is 15 frames at 120 BPM and 30 fps, and each scene is a whole number of beats.

## What makes it work

- **The real interface is the hero.** Scenes 3 to 7 run in the real frontend, answered from scripted snapshots.
- **Cuts land on the beat.** Music and picture share the same frame numbers.
- **The camera does the explaining.** It pushes into what matters, and each caption states one thing that just happened.
- **Each caption is a plain fact.** Check new lines against the project's plain-copy rule: no slogans, no "X, not Y", nothing vague.
- **Tension first.** The chores pile up over a tightening pulse, the music cuts dead, and the band starts when the mark lands. From there it plays unbroken to the ending; a stopped band was v4's biggest sound problem.
- **Small character.** The mark squashes when it lands and blinks. The sticker captions tilt and overshoot.

## Refilm

The film doesn't record a live session. `capture.mjs` answers `/api/*` in the browser from `run.json` and steps the real frontend frame by frame. A frame of the clip is always the same frame of the film: `desktop.mp4` starts at film frame 540 and `phone.mp4` at 1485. Nothing needs re-timing after a refilm.

Start the frontend from the repo root, in a shell, because the preview launcher can't read `~/Documents`:

```bash
cd web && bun install --frozen-lockfile && ./node_modules/.bin/next dev -H 127.0.0.1 --port 3211
```

Then from `demo/`:

```bash
node capture/write-run.mjs
```

```bash
npm run capture
```

Pass `desktop` or `phone` after `node capture/capture.mjs` to film just one.

Details that were hard won:

- Scene 5 is one unbroken clip. During frames 1305 to 1365 the capture narrows the real window around the Acme message, keeping that message on the same pixel, and writes each frame's window to `public/clips/markers.json` for the film's mask. It logs `drift` if the message moves.
- The chat window is filmed small (1040 by 560 CSS pixels) at camera zoom 1.5, so the whole window fits with readable text. The film scales the phone around the Acme message: zoom 1.5 through the morph, then back to show the whole phone once it has formed.
- The numbers are short bullets so they wrap the same on desktop and phone. The desktop page is 1200 px tall, with padding around the window, so the phone-sized window never runs off the page.
- Animations are off in the page. Pops, the panel's slide and the cursor are set per frame, and a frame where nothing changed reuses the last image, so holds are perfectly still.
- `Element.scrollTo` is disabled, so the film alone decides where the thread sits. On desktop the capture eases the thread to the newest message each frame. On the phone extra bottom padding stops the frontend pinning it to the bottom.
- Setting `processing` in a snapshot turns on the frontend's real "Nori is thinking" line. Its mark and dots are moved per frame because page animations are off.
- Dashed edges mark anything that would write, and turn solid on the tap. The invite card, the proposal card and page, and the tracker card aren't frontend features: the capture adds them to the page. The lock screen and the tracker are drawn in `src/scenes.tsx`.
- Find the composer with `getByLabel("Message Nori")`. Send typed messages before applying their snapshot, or the thread shows them twice.

`record/` and `public/real/` belong to the earlier film, which recorded a live run. Live runs use production and real money: check the root `AGENTS.md` first.

## Check the render

Look at frames every time. Captions landing before the event, or covering the text they describe, were the most common defects. Also check `SCRIPT.md`'s finish standard.

```bash
ffmpeg -y -loglevel error -i out/nori.mp4 -vf "fps=1/2,scale=384:-1,tile=6x4" -frames:v 1 /tmp/sheet.png
```

```bash
npx remotion still NoriFilm /tmp/frame.png --frame=700 --scale=0.5
```

```bash
ffmpeg -hide_banner -i out/nori.mp4 -af volumedetect -vn -f null - 2>&1 | grep -E "mean_volume|max_volume"
```

```bash
ffmpeg -hide_banner -i out/nori.mp4 -vf "freezedetect=n=0.002:d=1.2" -an -f null - 2>&1 | grep freeze_start
```

Check every caption at its first and last frame, each segment boundary, and any figure on screen. An agent can't hear the mix, so Austin has to judge the audio.

The creative brief is fun, informative and snappy, using 2026 marketing techniques, and "Don't be boring."
