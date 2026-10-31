# Nori film

Everything needed to change or reshoot the 48-second Nori film. Austin approved this version on 2026-10-31 and wants it as the quality bar: tweak from it rather than starting over.

## Start here

Most changes only need a re-render. Decide which of these you need:

| Change | What to do | Live spend |
| --- | --- | --- |
| Copy, captions, camera, timing, deck scenes, music | [Edit and render](#edit-and-render) | None |
| Different moments from the same run, or new UI behaviour on the same data | [Refilm the replay](#refilm-the-replay) | None |
| Nori has to do something it didn't do on October 31 | [Record a new run](#record-a-new-run), then refilm | About $3 |

The video files stay out of Git. The approved take is at `~/Documents/dev/agents/nori-film-media/2026-10-31/`, and worktree cleanup can't touch it.

## Setup

```bash
cd demo && npm ci
```

```bash
npx skills experimental_install
```

The second command restores the pinned Remotion skills from `skills-lock.json`. If it fails, use `npx skills add remotion-dev/skills`. Read `.agents/skills/remotion-best-practices` before writing Remotion code.

Restore the approved footage, because `src/timeline.json` is cut to that exact take:

```bash
M=~/Documents/dev/agents/nori-film-media/2026-10-31; mkdir -p public/clips out; cp $M/desktop.mp4 $M/phone.mp4 $M/markers.json public/clips/
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

`npm run dev` opens Remotion Studio. `npm run render` writes `out/nori.mp4` at CRF 18. Run `npm run score` after any timing change, because the music and effects read the timeline and markers.

| File | Holds |
| --- | --- |
| `src/timeline.json` | Scene lengths, and each demo segment's source range, length, caption, caption position and camera moves |
| `src/scenes.tsx` | The seven scenes and the deck copy |
| `src/kit.tsx` | Colours, Figtree, the pop animation, the real Nori mark, the deck's ribbons |
| `capture/score.mjs` | The synthesized music and picture-locked effects |
| `capture/capture.mjs` | The replay and the filming |
| `capture/run.json` | The 109 real session snapshots |
| `public/real/` | The real task screenshot |
| `record/` | Scripts that drive and record a new live run |
| `sources/` | The pitch deck and the Docusign Q2 release used to check figures |

Keep the scene lengths on the beat. One beat is 15 frames at 120 BPM and 30 fps, and each scene is a whole number of beats.

## What makes it work

- **The real product is the hero.** 24 of the 48 seconds are the real interface showing a real run. Nothing in that footage is staged except the typing.
- **Cuts land on the beat.** The picture and sound share `timeline.json`, so every cut, pop and whoosh stays locked.
- **The camera does the explaining.** It pushes into what matters, and each caption states one thing that just happened.
- **Each caption is a plain fact.** "Hit its $1 budget. So it asked." Check new lines against the project's plain-copy rule: no slogans, no "X, not Y", nothing vague.
- **Tension first.** The cold open piles up empty chats, then "It forgot you. Again." The score starts as a tense pulse and drops when the mark lands.
- **Small character.** The mark squashes when it lands and blinks. The sticker captions tilt and overshoot.

| Scene | Frames | Source |
| --- | --- | --- |
| Cold open | 0–120 | Written for the film |
| 70% stat | 120–240 | Deck, with its Salesforce citation on screen |
| Meet Nori | 240–330 | Deck ribbons and the real mark |
| Demo | 330–1050 | The real run |
| Phone | 1050–1170 | The same run on the real mobile layout |
| Trust | 1170–1290 | Deck claims, not footage |
| End | 1290–1440 | Deck's closing line |

## Truth rules

- Every message, task event, figure and result in the demo must come from a real run. Label it on screen with the date and "Sped up."
- Don't caption behaviour the run didn't show. "It remembered" was cut because Nori said it can't save lasting preferences yet: the rule held inside this thread only.
- Check any figure a caption vouches for against its source. Q2 figures match `sources/docusign-2q27-earnings-release.pdf`. The Rune deal and the October 13 Salesforce launch were confirmed; the $375M is shown as reported. The stock prices in the news brief were never checked.
- Write "Docusign" in anything we author. Nori's own replies say "DocuSign", and footage stays as Nori wrote it.
- Real footage is public-facing, so check snapshots for email addresses and private data before filming.

## Refilm the replay

The film doesn't record a live session. The hosted site's sign-in blocks automation, so `capture.mjs` answers `/api/*` in the browser from `run.json` and plays each snapshot through the real frontend, typing the real messages.

Start the frontend from the repo root:

```bash
cd web && bun install --frozen-lockfile && ./node_modules/.bin/next dev -H 127.0.0.1 --port 3211
```

Then from `demo/`:

```bash
npm run capture
```

Run the dev server from a shell. The preview launcher can't read `~/Documents`. Pass `desktop` or `phone` after `node capture/capture.mjs` to film just one.

**A new take moves the timing.** A test refilm drifted up to a second from the approved take, which is enough to break the captions. Don't refilm for an edit that doesn't need new footage. If you do refilm, re-derive every `src` range in `timeline.json` from the new `public/clips/markers.json` and check it by eye. Interface updates appear a beat after their marker, so start a segment later than the marker says. Run `npm run score` afterwards.

Details that were hard won:

- Screencasts ignore device scale. Desktop films at 2560×1600 with `html{zoom:2}` for sharp text.
- A `Date.now` offset gives real timestamps, and an `online` event forces the frontend to refresh.
- Find the composer with `getByLabel("Message Nori")`, since `textarea.last()` hits the panel's task box. Find the panel with `aside[aria-label="Agent panel"]`.
- `capture.mjs` draws its own cursor, because headless screencasts don't.
- The script looks up messages by their opening words and the Q2 task by ID (`b2b631a1`). Update these after a new run.

## Record a new run

This uses production and real money. Check the root `AGENTS.md` first, especially the cost and model rules: it changes more often than this page. Don't change a model to make a shot work.

**Check the models and budget.** The October 31 run used Opus 5.5 for the conversation and `gemini-3.8-flash` for tasks. Main has since selected other models, so use whatever `AGENTS.md` says now and expect different costs and wording. That run cost about $2.89: $1.07 on OpenRouter and $1.82 for two tasks on Vertex. An exhausted OpenRouter key returns HTTP 403 from the conversation. Austin controls the key's limit, and topping up credits isn't enough if the limit is the cap.

**Bring production up.** From `core/`, in a worktree that hasn't been initialized yet, and with the state paths below:

```bash
terraform -chdir=infrastructure/poc/terraform init -input=false -backend-config=path=$HOME/.local/state/nori/bot-111549/poc/terraform.tfstate
```

```bash
terraform -chdir=infrastructure/temporal/terraform init -input=false -backend-config=path=$HOME/.local/state/nori/bot-111549/temporal/terraform.tfstate
```

```bash
NORI_ENV_FILE=/absolute/path/to/main/.env python3 infrastructure/poc/poc.py unpause
```

**Record in the background.** From `demo/record/`:

```bash
python3 rec.py 3000
```

**Send the script.** Use `python3 say.py "..."`, and `python3 show.py` to read progress from saved snapshots. Don't poll the runtime any other way: that caused 429s on October 31. These are the messages that made the film, in order:

1. `Thanks. So for this thread: briefs under 200 words, numbers first.`
2. `Pull Docusign's Q2 results from the investor site and write me a brief.`
3. `Also add the full-year guidance.`
4. `Different question. One line to reopen a renewal with a customer who's gone quiet?`
5. `Start a second task: find the latest Docusign and Salesforce news.`
6. When the budget question arrives: `Continue. If the site keeps blocking you, the release PDF is at https://s21.q4cdn.com/706790701/files/doc_earnings/2027/q2/earnings-result/2Q27-Earnings-Release.pdf`
7. `Give me the full-year guidance in one line I can paste.`

Send 2 to 5 within about a minute so they overlap. The thread is real: these messages stay in Austin's history, and approving a budget spends real money.

**Shut it down as soon as the run ends.** Then confirm `paused: true` and that `nori-computer` is stopped:

```bash
NORI_ENV_FILE=/absolute/path/to/main/.env python3 infrastructure/poc/poc.py pause
```

**Build the data and refilm.** Use your run's first filmed message and task IDs, then copy the task screenshots the film opens into `public/real/`:

```bash
python3 build_run.py "Thanks. So for this thread" b2b631a1 64894e8c
```

## Check the render

Look at frames every time. Captions landing before the event, or covering the text they describe, were the most common defects.

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

The film's creative brief was: fun, informative and snappy, using 2026 marketing techniques, real data rather than test data, and "Don't be boring."
