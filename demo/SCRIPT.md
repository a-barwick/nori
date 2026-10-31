# Nori film v5: script

The approved script for the current film. It runs about 1:31. It opens the pitch, and the deck carries trust, data and the ask.

**The premise:** Nori reads and proposes on its own, and writes only when you say so. The film shows this; no card states it.

## Decisions

- **Hype film.** Scenes 3 to 7 are scripted, with fictional people and data, played through the real frontend from `capture/run.json`. No live run and no disclaimer.
- **Dashed means it would write.** The table and invite and the proposal are dashed until Maya taps, then solid. Reading and drafting just happen. No caption explains this.
- **Every Nori message is conversational.** No orphaned facts: each line reads as something a person would say in that moment.
- **Nori never looks frozen.** The frontend's "Nori is thinking" line, with moving dots, runs while she waits for a reply.
- **Hold rule.** Anything people must read holds for at least 0.4 s per word, with a 1.5 s minimum. Two deliberate exceptions, because waiting on them felt awkward: the Kaze card and the Acme bullets.
- **One piece of music.** The band runs unbroken from the drop to the ending and only gets lighter under the morning. Effects use notes from the chord playing at that moment.
- **New surfaces, film only for now:** the invite card, notification, proposal preview and page, and tracker card.
- **Keep from earlier films:** Figtree, the colour palette, sticker captions, overshoot pops, ribbons, the real Nori mark, and cuts on the beat.

## Settled in review

A marketing review of the storyboard challenged several things. Austin decided these, so don't reopen them without a new reason.

**Taken from the review**

- Cut ideas first, then apply the hold rule.
- Nori asks before booking. Maya taps to book the table and send the invites.
- Maya opens the proposal before she sends it.
- Have an answer ready if the room asks about reading call data.
- No detail about a teammate's diet on the invite card.
- Give build-and-share more weight, with something more app-like than tiles.

**Rejected**

- Turning every unprompted step into a proposal. That would make Nori a nag, so reading and drafting just happen.
- Cutting the weather ask. Unrelated "also" messages show the one thread and natural flow. Only the weather tile went.
- "Jobs finish in the background." It's engineer-speak.
- A cold "Morning. I have the proposal ready." Keep the warm opener and the 🍣.
- Cutting "You didn't ask." It stays because what comes before it is reading and drafting.
- "Nori works on its own computer for you." It's clunkier and doesn't answer the unmanaged-compute worry.
- "It builds dashboards." It's too narrow for GTM's ask to build something and share it.

**Other calls**

- "Pick a model." is out, so scene 1 can't mock Nova if Nova has a model picker. Nobody has confirmed whether it does.
- Sam's cursor replaces "Sam opened it.", which read as surveillance.
- The film-only surfaces are limited to ones Austin would commit to building. The notification is the least certain of them.

**Changed from v4, after Austin watched it**

- The opening card is new, because the first frame was too abrupt.
- Three walls press the mess into the rice. The bamboo mat is gone: mats make rolls, and onigiri is pressed.
- The trust slide is gone. "Nothing goes out until you tap." lands on the send instead.
- The 8:30 tap is gone, so the morning is one message and one card.
- Nori's morning draft is already written when she opens it. Drafting doesn't send anything.
- Sunset colours are gone. The phone scene stays lavender, with the film's purple overnight.

**Still open:** Austin's review of the mix.

## Timing

120 BPM at 30 fps. One beat is 15 frames.

| # | Scene | Time | Frames |
|---|---|---|---|
| 0 | Opening card | 0:00–0:02.5 | 0–75 |
| 1 | The problem | 0:02.5–0:12 | 75–360 |
| 2 | Meet Nori | 0:12–0:18 | 360–540 |
| 3 | Just ask | 0:18–0:27.5 | 540–825 |
| 4 | It handles it | 0:27.5–0:43.5 | 825–1305 |
| 5 | Same thread | 0:43.5–0:48.5 | 1305–1455 |
| 6 | The next morning | 0:48.5–1:12 | 1455–2160 |
| 7 | Her own computer | 1:12–1:25 | 2160–2550 |
| 8 | Close | 1:25–1:31.5 | 2550–2745 |

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
| 0 | Purple card, ribbons draw on, a quiet pulse begins | "The problem" |
| 60 | The card lifts off the waiting chat app | |
| 75 | The cursor clicks New chat | "New chat." |
| 135 | Three files land | "Add the files." |
| 195 | Text pours into the box | "Explain it again." |
| 285 | Windows stack and tilt | "Tomorrow, do it all again." |
| 360 | The pulse cuts dead, then three purple walls slam in, left at 375, right at 390, bottom at 405, squeezing the mess white | |
| 413 | The triangle rounds into rice with a pat, and pats again at 435 | |
| 450 | Seaweed, bass drop, purple, the eyes open | "Nori" / "The assistant inside Nova" |
| 540 | The purple wipes to the whole app window: both sides of the chat and the message box. She types and sends at 585 | "Grab numbers for the Acme account" |
| 585 | Nori thinks for a moment | "Nori is thinking" |
| 600 | Nori replies, with 1 task in progress | "On it!" |
| 627 | She types and sends at 660 | "Also find a dinner spot near their office" |
| 663 | She types and sends at 690. Nori thinks | "Also what's the weather there?" |
| 750 | Nori replies, and the line says 3 tasks in progress | "Sure, adding both." |
| 765 | Caption | "All in one thread." |
| 825 | Nori | "It's looking chilly in Chicago tonight, 48° and windy. Bring a coat! 🧥" |
| 975 | Nori. The dashed card arrives at 990 with a map, two pins and five faces | "Found a spot! Kaze is two blocks from their office." / "Table for 6, 7PM" / "Book it and invite the team" |
| 1065 | The cursor arrives and clicks. The edge turns solid and faces get a check, one per beat from 1080 | "Booked. Invites sent." |
| 1140 | Nori | "By the way, got those Acme numbers:" / "$1.2M a year, up 18%" / "340 of 400 seats active" / "Renews March 14" |
| 1305 | One unbroken shot: the window narrows into a phone around the still bullets, on lavender, with the band lighter underneath | |
| 1395 | Slow fade and rise | "Same thread." |
| 1455 | The screen goes dark, the background turns purple, the clock races overnight, and the mark pops at 1490, 1513 and 1536 | The clock |
| 1545 | 8:00 AM. The lock screen lights, with a double buzz and a notification at 1549 | "Morning! Hope dinner went well. 🍣" |
| 1620 | She taps and it opens into the thread | |
| 1615 | Nori, under the lock screen until it fades | "Morning! Hope dinner went well. 🍣 Priya asked about multi-year pricing twice, so I drafted her a proposal." |
| 1650 | The dashed proposal card arrives | "Acme renewal proposal" / "For Priya. Not sent." / "Take a look" |
| 1770 | Caption | "You didn't ask." |
| 1860 | She taps and the proposal opens as a dashed page | "Acme renewal proposal", the pricing table, "Send to Priya" |
| 1950 | She taps. The page goes solid and the full band returns. It closes at 2025 | "Sent to Priya" / "Nothing goes out until you tap." |
| 2055 | She types and sends at 2115. Nori thinks | "Amazing. Turn those numbers into an app for Sam." |
| 2160 | The camera flies through the phone into a desktop with a menu bar, dock and empty window | |
| 2193 | Caption, on the desktop | "Your Nori has its own computer." |
| 2265 | The Renewal tracker builds on the beat: title, filter, three rows, detail chart | "Renewing soon" |
| 2268 | Caption | "It builds dashboards and apps." |
| 2400 | Sam's cursor arrives and clicks Globex at 2430. The detail panel follows | |
| 2460 | The app shrinks into a card on the phone | "Renewal tracker. Sam has it." |
| 2550 | The band drops out | "We delivered chat." |
| 2595 | Second chord, and the ribbons draw on | "Now let's deliver an assistant." |
| 2670 | The mark pops and smiles. It blinks at 2715 and the music fades to the end at 2745 | "Nori, inside Nova." |

## Finish standard

Check rendered frames, not just the code:

- Scenes 1 and 3 read with no sound.
- The message box and both sides of the chat stay in frame throughout scene 3.
- The bullets in scene 5 don't shift until the phone has formed. Step through frames 1305 to 1365.
- The final mark in scene 2 matches `Onigiri` in `src/kit.tsx` exactly.
- Captions never cover the text they describe, and arrive with or after the event.
- All copy matches this page word for word.
- Austin reviews the audio, since an agent can't hear it.
- Keep video files out of Git. Save each take to a new folder under `~/Documents/dev/agents/nori-film-media/`.
