# Nori film v3: script

The approved script for the next version of the film. It is 59 seconds: Maya's chat chores, then Nori, then one day of work, then the ask.

Austin approved the 2026-10-31 film's look, theme and animation, and that quality is the bar. This script changes the content and order. Keep everything else.

## Decisions already made

Build these as written. Don't reopen them.

- **It's a hype film.** Scenes 3 to 7 are scripted, with fictional people and data. Don't add a disclaimer, and remove the "Recorded from a real Nori run" label. This replaces the truth rules in `README.md`.
- **No live run.** Write scripted snapshots in the same format as `capture/run.json`, and play them through the real frontend with `capture/capture.mjs`. No production or model spend is needed.
- **Cut:** the 70% stat, "It forgot you. Again.", the Q2 brief, the bot check and the budget question.
- **Don't add:** an automatic memory caption, the "why now" launches, or time back for each team.
- **Keep from the current film:** Figtree, the colour palette, sticker captions, overshoot pops, push-in camera moves, ribbons, the real Nori mark, and the cut-to-the-beat approach with its music style.

## Timing

120 BPM at 30 fps. One beat is 15 frames, one bar is 60 frames. Every major hit below falls on a beat.

| # | Scene | Time | Frames |
|---|---|---|---|
| 1 | The problem | 0:00–0:05 | 0–150 |
| 2 | Meet Nori | 0:05–0:10 | 150–300 |
| 3 | Just ask | 0:10–0:16 | 300–480 |
| 4 | It handles it | 0:16–0:27 | 480–810 |
| 5 | Same thread | 0:27–0:31 | 810–930 |
| 6 | The next morning | 0:31–0:41 | 930–1230 |
| 7 | Her own computer | 0:41–0:49 | 1230–1470 |
| 8 | Trust | 0:49–0:53 | 1470–1590 |
| 9 | Close | 0:53–0:59 | 1590–1770 |

## The cast

| Name | Who | Details |
|---|---|---|
| Maya | The user, an enterprise account executive | Seen only through her messages |
| Acme | Her customer, in Chicago | $1.2M a year, up 18%. 340 of 400 seats active. Renewal March 14 |
| Priya | Her contact at Acme | Receives the renewal proposal |
| Sam | Her teammate | Receives the dashboard |
| Kaze | A fictional sushi restaurant | Two blocks from Acme's office |

## Scene 1: The problem (0:00–0:05)

**The point:** chat apps make you do setup before you can ask anything.

An unbranded chat app fills the screen: a sidebar on the left and a message box with "How can I help?". Maya's cursor does each chore, one every 30 frames. Each action is faster and more frantic than the last, and a sticker caption pops in with each.

| Frame | What happens | Caption |
|---|---|---|
| 0 | The cursor clicks "+ New chat" and a blank chat appears | "New chat." |
| 30 | A model menu drops open: Fast, Balanced, Advanced | "Pick a model." |
| 60 | Three files land above the box: `Acme_notes.pdf`, `Q2_numbers.xlsx`, `Renewal_call.docx` | "Add the files." |
| 90 | Text pours in: "Acme is a renewal customer. Their contract ends in March. Last time we talked about pricing, and I like short emails, and..." | "Explain it again." |
| 120 | The sidebar overflows: Acme email, Acme email (2), Q2 numbers, Renewal new try, Untitled chat. Windows stack and tilt until the screen is a mess | "Tomorrow, do it all again." |

**Sound:** the tense pulse from the current cold open, tightening. A pop on each caption and clicks for the cursor.

**Into scene 2:** at frame 150 the music cuts dead and the mess stops moving.

## Scene 2: Meet Nori (0:05–0:10)

**The point:** Nori takes all of that away, and it's adorable about it.

| Frame | What happens | Sound |
|---|---|---|
| 150–195 | A bamboo sushi mat slides in and rolls up the whole mess: menus, files, windows and all. It squishes into a ball of white rice | Near silence, then a soft squish |
| 195, 210, 225 | The ball is patted into a triangle, a little sharper on each beat, with a small squash each time | One light tap per beat |
| 240 | A sheet of seaweed slaps onto the bottom. The background snaps to the purple from the current Meet scene. The result is exactly the real Nori mark. The eyes pop open | **The bass drop and full music** |
| 240–300 | The ribbons draw on. "Nori" and "The assistant inside Nova" pop in. The mark smiles, then blinks | Full music |

**Into scene 3:** on frame 300 the purple wipes away from the mark's centre to reveal the real Nova interface, clean and empty, with the cursor in the message box.

## Scene 3: Just ask (0:10–0:16)

**The point:** she just says what she needs, however it comes to her.

Use the real interface and a push-in camera. Keep the messages exactly as written.

| Frame | What happens | Sound |
|---|---|---|
| 300–345 | She types: **"Grab numbers for the Acme account"** and sends it | Key ticks, send pop |
| 345–375 | **Everything holds.** No reply, no movement | Music falls to a pulse |
| 375 | Message pops in, without typing: **"Also find a good spot for dinner near their office"** | Pop |
| 405 | Message pops in, without typing: **"Also what's the weather like over there?"** | Pop |
| 435 | Nori replies: **"Not a problem! On it. I'll make that invite big enough for the whole team."** | Music returns |
| 450–480 | Caption: **"Three asks. One thread."** | Pop |

**Into scene 4:** a straight cut on frame 480, the camera keeping the thread framed. Show the messages arriving one by one, with no reply until frame 435.

## Scene 4: It handles it (0:16–0:27)

**The point:** three unrelated jobs land in one place, and she manages nothing.

| Frame | What happens | Caption |
|---|---|---|
| 480 | Nori: **"Chicago: 48° and windy tonight. Bring a coat."** | "Quick answers come right back." |
| 540 | The Activity panel slides open with two tasks running: "Find dinner near Acme" and "Pull Acme numbers" | "The rest keep running." |
| 630 | The panel closes. Nori: **"Table for six at Kaze, 7:00 PM, two blocks from their office. There's a vegan menu. The invite's on everyone's calendar."** | "The whole team's invited." |
| 720 | Nori: **"Acme is at $1.2M a year, up 18%. 340 of 400 seats are active. Renewal is March 14."** A document card appears below it | "Done. All in one thread." |
| 780–810 | The caption clears and the camera settles until this last message is centred | |

**Sound:** full music, with a pop on each Nori reply.

**Into scene 5:** the last message stays exactly where it is.

## Scene 5: Same thread, the "Apple moment" (0:27–0:31)

**The point:** the thread goes with her. This is the film's most elegant moment, and it should feel calm and expensive after the bouncy demo.

This is one unbroken shot with no cut. The Acme message must not move on screen until the phone has formed.

| Frame | What happens | Sound |
|---|---|---|
| 810 | The music falls away to one sustained note | One held note |
| 810–870 | The desktop edges glide smoothly inward. The corners round. Sidebar and header fade out as the text reflows to phone width | The note continues |
| 870–900 | A phone body settles around the screen, turning slightly in 3D. The background darkens to warm evening | A soft, single tap at 900 |
| 900–930 | Large, quiet text: **"Same thread."** No bounce on this one: a slow fade and slight rise | The note fades |

Build it so the viewer can't find the join between desktop and phone footage: the same message, at the same place and size, while the frame changes around it.

**Into scene 6:** the phone doesn't move.

## Scene 6: The next morning (0:31–0:41)

**The point:** the biggest moment in the film. Nori does something she never asked for.

| Frame | What happens | Sound |
|---|---|---|
| 930–990 | The phone stays still. Its screen dims and goes dark. The background moves from evening through night to sunrise, and a clock races to **8:00 AM** | Almost silent, with a soft rising tone |
| 990 | The phone buzzes on the beat and Nori's message appears, unprompted | Double buzz |
| 990–1110 | The camera pushes in slowly as the message's lines appear one per beat | Light pulse returns |
| 1110 | A single **Send it** button pops in, with the mark beside it | Pop |
| 1125–1170 | Caption: **"You didn't ask."** Hold it long enough to land | Beat drops out, then one hit |
| 1170 | Maya taps **Send it**. It changes to **"Sent to Priya at Acme."** | **Full music returns on the tap** |
| 1200 | Maya's reply starts typing (see below) | Key ticks |

Nori's message, exactly:

> Morning! Hope dinner with Acme went well. 🍣
>
> While you were out:
> - Pulled the three things they raised on your last calls
> - Drafted the renewal proposal around them
> - Moved your 9:00 so you have time to read it
>
> Ready to send when you are.

**Into scene 7:** her message, sped up to send by frame 1230: **"Amazing. Now turn the Acme numbers into a dashboard and share it with Sam."**

## Scene 7: Her own computer (0:41–0:49)

**The point:** every user has their own computer, so Nori can build things and not just write them.

| Frame | What happens | Caption | Sound |
|---|---|---|---|
| 1230–1260 | The camera flies through the phone screen into a full desktop | | Whoosh |
| 1260 | A desktop appears with the Nori mark in the corner of the window | **"Everyone gets their own computer."** | Hit |
| 1290–1380 | An "Acme renewal" dashboard builds itself on the beat: revenue tile ($1.2M), growth tile (+18%), seats gauge (340/400), a renewal date tile (March 14), then a chart line that draws itself | **"It builds apps and dashboards too."** | A pop per tile |
| 1380–1410 | The camera flies back out, the dashboard shrinking into a card in the thread on her phone | | Reverse whoosh |
| 1410 | Card: **"Acme renewal dashboard. Shared with Sam."** | | Pop |
| 1440 | A small line appears under it: **"Sam opened it."** | | Soft pop |

**Into scene 8:** a whoosh on frame 1470, with the card flying off.

## Scene 8: Trust (0:49–0:53)

**The point:** it's safe to use at work.

White background. Four cards in the current Trust style land one every 15 frames from 1470, then hold. Titles only, with no body text:

1. **Uses your permissions**
2. **Asks before acting**
3. **Logs every action**
4. **Apps stay private**

**Into scene 9:** a whoosh on frame 1590.

## Scene 9: Close (0:53–0:59)

Use the current End scene, with its timing adjusted to these frames.

| Frame | What happens | Sound |
|---|---|---|
| 1590 | **"We delivered chat."** | Band drops out, one chord rings |
| 1620 | **"Now let's deliver an assistant."** The ribbons draw on | Second chord |
| 1680 | The mark pops in and smiles | Bell notes |
| 1695 | **"Nori, inside Nova. Six-week pilot. One sales team."** | |
| 1740 | The mark blinks | Music fades to the end at 1770 |

## Finish standard

Check rendered frames, not just the code:

- Scenes 1 and 3 read with no sound.
- The Acme message in scene 5 doesn't shift until the phone has formed. Step through frames 810 to 870.
- The final mark in scene 2 matches `Onigiri` in `src/kit.tsx` exactly.
- Captions never cover the text they describe, and arrive with or after the event.
- All copy matches this page word for word. Write "Docusign" with a lowercase s anywhere it appears.
- Scene lengths match the timing table, and the music is rebuilt in `capture/score.mjs` to match the cues above.
- Austin reviews the audio, since an agent can't hear it. Send a render at each milestone: scenes 1–2, then 3–5, then the full film.
- Keep video files out of Git. Save the finished take to a new dated folder under `~/Documents/dev/agents/nori-film-media/`, and don't overwrite `2026-10-31`.
