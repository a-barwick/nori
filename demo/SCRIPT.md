# Nori film v4: script

The approved script for the current film. It runs about 1:50 and can be trimmed later. It opens the pitch, and the deck carries trust, data and the ask.

**The premise:** Nori reads and proposes on its own, and writes only when you say so. The wording on the Trust cards is still open.

## Decisions

- **Hype film.** Scenes 3 to 7 are scripted, with fictional people and data, played through the real frontend from `capture/run.json`. No live run and no disclaimer.
- **Dashed means it would write.** The table and invite, the 8:30 hold and the proposal are dashed until Maya taps, then solid. Reading and drafting just happen. No caption explains this.
- **Hold rule.** Anything people must read holds for at least 0.4 s per word, with a 1.5 s minimum.
- **New surfaces, film only for now:** the invite card, notification, proposal preview and page, and tracker card.
- **Keep from earlier films:** Figtree, the colour palette, sticker captions, overshoot pops, push-in camera moves, ribbons, the real Nori mark, and cuts on the beat.

## Timing

120 BPM at 30 fps. One beat is 15 frames.

| # | Scene | Time | Frames |
|---|---|---|---|
| 1 | The problem | 0:00–0:09.5 | 0–285 |
| 2 | Meet Nori | 0:09.5–0:16 | 285–480 |
| 3 | Just ask | 0:16–0:29.5 | 480–885 |
| 4 | It handles it | 0:29.5–0:48 | 885–1440 |
| 5 | Same thread | 0:48–0:53 | 1440–1590 |
| 6 | The next morning | 0:53–1:23 | 1590–2490 |
| 7 | Her own computer | 1:23–1:37 | 2490–2910 |
| 8 | Trust | 1:37–1:42.5 | 2910–3075 |
| 9 | Close | 1:42.5–1:50.5 | 3075–3315 |

## The cast

| Name | Who | Details |
|---|---|---|
| Maya | The user, an enterprise account executive | Seen only through her messages |
| Acme | Her customer, in Chicago | $1.2M a year, up 18%. 340 of 400 seats active. Renews March 14 |
| Priya | Her contact at Acme | Receives the renewal proposal |
| Sam | Her teammate | Uses the app |
| Kaze | A fictional restaurant | Two blocks from Acme's office |
| Globex, Initech | Fictional accounts | Fill out the renewal tracker |

## Scene by scene

| Frame | What happens | Words on screen |
|---|---|---|
| 0 | An unbranded chat app. The cursor clicks New chat | "New chat." |
| 60 | Three files land | "Add the files." |
| 120 | Text pours into the box | "Explain it again." |
| 210 | Windows stack and tilt | "Tomorrow, do it all again." |
| 285 | Music cuts dead. The bamboo mat rolls up the mess, and the rice is patted into a triangle | |
| 375 | Seaweed, bass drop, purple, the eyes open | "Nori" / "The assistant inside Nova" |
| 480 | The purple wipes to the real interface. She types and sends at 525 | "Grab numbers for the Acme account" |
| 570 | Message pops in | "Also find a dinner spot near their office" |
| 690 | Message pops in | "Also what's the weather there?" |
| 780 | Nori replies, and the music returns | "On it!" |
| 795 | Caption | "One thread for everything." |
| 885 | Nori | "48° and windy in Chicago tonight. Bring a coat! 🧥" |
| 1020 | Nori. The dashed invite card arrives at 1032 with a map, two pins and five faces | "Kaze has a table for 6 at 7PM, two blocks from their office." / "Book it and invite the team" |
| 1200 | Maya clicks. The edge turns solid and faces get a check, one per beat from 1215 | "Booked. Invites sent." |
| 1275 | Nori | "$1.2M a year, up 18%" / "340 of 400 seats active" / "Renews March 14" |
| 1440 | One unbroken shot: the window narrows into a phone around the still bullets, over one held note | |
| 1530 | Slow fade and rise | "Same thread." |
| 1590 | The screen goes dark, the clock races overnight, and the mark pops at 1625, 1648 and 1671 | The clock |
| 1680 | 8:00 AM. The lock screen lights, with a double buzz and a notification at 1684 | "Morning! Hope dinner went well. 🍣" |
| 1755 | She taps and it opens into the thread | |
| 1770 | Nori | "Priya asked about multi-year pricing on two of your last three calls." |
| 1950 | Nori | "Proposal's ready." |
| 1995 | Dashed chip. She taps at 2070 and it turns solid | "Hold 8:30 to read it?" / "8:30 held" |
| 2115 | The dashed proposal card arrives | "Renewal proposal" / "To Priya at Acme" |
| 2145 | Caption, with the beat out | "You didn't ask." |
| 2220 | She taps and the proposal opens as a dashed page | "Acme renewal proposal", the pricing table, "Send to Priya" |
| 2310 | She taps. The page goes solid and full music returns. It closes at 2340 | "Sent to Priya" |
| 2385 | She types and sends at 2445 | "Amazing. Turn those numbers into an app for Sam." |
| 2490 | The camera flies through the phone into a desktop with a menu bar, dock and empty window | |
| 2523 | Caption, on the desktop | "Your Nori has its own computer." |
| 2610 | The Renewal tracker builds on the beat: title, filter, three rows, detail chart | |
| 2613 | Caption | "It builds dashboards and apps." |
| 2745 | Sam's cursor arrives and clicks Globex at 2775. The detail panel follows | |
| 2805 | The app shrinks into a card on the phone | "Renewal tracker. Shared with Sam." |
| 2910 | A whoosh and three cards, one every two beats | "Uses your permissions" / "Proposes on its own" / "Writes when you say so" |
| 3075 | The band drops out | "We delivered chat." |
| 3120 | Second chord, and the ribbons draw on | "Now let's deliver an assistant." |
| 3195 | The mark pops and smiles. It blinks at 3268 and the music fades to the end at 3315 | "Nori, inside Nova." |

## Finish standard

Check rendered frames, not just the code:

- Scenes 1 and 3 read with no sound.
- The bullets in scene 5 don't shift until the phone has formed. Step through frames 1440 to 1500.
- The final mark in scene 2 matches `Onigiri` in `src/kit.tsx` exactly.
- Captions never cover the text they describe, and arrive with or after the event.
- All copy matches this page word for word.
- Austin reviews the audio, since an agent can't hear it.
- Keep video files out of Git. Save each take to a new folder under `~/Documents/dev/agents/nori-film-media/`.
