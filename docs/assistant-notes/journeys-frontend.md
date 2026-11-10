# Journeys driven from the web (frontend)

Local stack on recordings at `041d5da`, merged into the frontend branch at `b755082`. Web on 3212, core on 8082. The test user's timezone is America/Los_Angeles and its locale en-US. Screenshots are in the frontend scratchpad under `shots/journeys/`; ask the frontend session to send any of them.

| # | Journey | Task | Result | Finding |
|---|----|----|----|----|
| 1 | Two Google accounts | — | Apps shows both accounts (`recorded.user@…` Ready, `recorded.work@…`) | Account B shows "Needs more access" for contacts and Drive: its recorded exchange grants fewer permissions than a new connection requests. |
| 1 | Told apart in a task ("work … Wednesday 18 November, and personal") | `a304b1dc` | failed | The model read B by calendar ID: `GET calendars/recorded.work@example.com/events` has no recording (only `calendars/primary` is recorded for B), so replay answered 501 and B went to "temporarily failing". A has no 18 Nov list, so it replayed the fixed list and Nori correctly refused it. **Recordings:** B by calendar ID, and A's list for 18 Nov. |
| 2 | Weekend agenda | `6de08547` | completed | Saturday: Weekend hike, all day; Sunday: nothing. |
| 2 | "Also Monday" | `29a69f42` | failed | The read for 16 Nov matched. The next model call then hung 15 minutes (openrouter `gpt-6.1-terra`, model.call.failed at 08:24) and the task told the user "The model result could not be saved." **Backend:** the wrong message for a model failure, and no time limit on the call. |
| 3 | Monday agenda before changes | `f7875e95` | completed | Standup, Lunch with Priya (Cafe Vita) and Design review (awaiting response) are listed. |
| 3 | "Move lunch with Priya to 1pm." | `0625fe36` | failed | `GET calendars/primary/events/reclunch` replayed **get-missing (404)**. The replay takes the first matching exchange in file order, and the wildcard `get-missing` (`events/*`) comes before `get-lunch`. **Integration:** prefer the most specific path, or move `get-missing` below the exact reads. |
| 4 | "Accept the design review." | `1490ea4f` | needs you (question) | Same 404 on `events/recdesign`; Nori then asked a question instead of patching. |
| 5 | "Move just this Monday's standup to 9:30." | `565d2513` | failed | Same 404 on `events/recstandup_20261116T170000Z`. |
| 5 | "Rename the standup series to Team sync." | `addff7e0` | failed | Same 404 on `events/recstandup`. |
| 5 | Monday agenda after | `0f5e27f7` | completed | Unchanged, as expected since nothing was patched. |
| 6 | Allow sending on account A | — | passed | Apps → account A → "Calendars, defaults and access" → "Not allowed yet" → Allow on Send email. The recorded mail code returned A as Ready. (New: with two accounts the per-capability Allow sits in each account's details, `ea53ff9`.) |
| 6 | "Find the email from Lilla Tavern about my reservation." | `1aefd717` | passed | Fri 13 Nov 7:30 pm Pacific, 418 Pine Street, party of 2, LT-58213, with a link to the email. |
| 6 | "Reply to it saying we'll be there at 7pm, and send it." | `df87c08a` | needs you (question) | `POST gmail.googleapis.com/gmail/v1/users/me/drafts` has no recording: the draft, send and sent recordings use host `www.googleapis.com`, but Gmail calls go to `gmail.googleapis.com` (search and get are recorded there). The draft's operation summary also reads `Draft "" to  (not sent)`, empty before the call. **Integration:** the host. **Backend:** the summary. |
| 7 | Reminder that follows a moved event | — | blocked | Needs journey 3's move, which is blocked by the `get-missing` ordering. |
| 8 | Repair mid-task on account B | — | blocked | The model reads B as `calendars/recorded.work@example.com/events`; the 401 and its retry are recorded only for `calendars/primary`. B is also left "temporarily failing" from journey 1. **Integration:** record B's events under its calendar ID, or match either. |
| all | Time zone | — | — | Task texts and results still say "Europe/London" although Preferences say America/Los_Angeles; tool summaries show PST. **Backend:** where the outer model gets the user's time zone. |
