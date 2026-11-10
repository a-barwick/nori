# A42 phone test

Austin runs this on his own phone against a local Nori stack on his Mac. It takes about 20 minutes. Production is not involved.

## Why HTTPS

A plain `http://192.168.x.y:3212` address doesn't work from a phone. Browsers send `Sec-Fetch-Site` only to secure origins, and Nori's same-origin check (`web/src/lib/runtime/auth.ts`) refuses requests without it. The microphone and push notifications also need a secure origin. So the phone reaches the Mac through Tailscale's private HTTPS address, which only devices on Austin's tailnet can open.

## Setup on the Mac

1. Pull the integration branch (it has the recordings, including the iCloud read-back after a create). Run the local stack with recorded providers: core on 8082 and the worker, both with `NORI_PROVIDER_REPLAY=<repo>/contracts/assistant/recordings`, the recordings root (see the runbook).
2. Find the Mac's tailnet name: `tailscale status --self` prints it, for example `austins-macbook-pro.tail1234.ts.net`. Call it HOST below.
3. Start the web server with HOST allowed. Run it from `web/`:
   ```bash
   NORI_LOCAL_AGENT_URL=http://127.0.0.1:8082 NORI_LOCAL_EMAIL=<local user email> NORI_DEV_ORIGINS=HOST NORI_APP_ORIGIN=https://HOST bun --no-env-file run dev --port 3212
   ```
4. Serve it on the tailnet:
   ```bash
   tailscale serve --bg 3212
   ```
5. Turn the phone's Tailscale on, then open `https://HOST` in Safari (or Chrome on Android).

While `NORI_DEV_ORIGINS` is set, any device that can reach HOST acts as the local user. Afterwards, turn both off:
```bash
tailscale serve --https=443 off
```
Then restart the web server without `NORI_DEV_ORIGINS` and `NORI_APP_ORIGIN`.

## Steps

| # | Do | What you should see |
|---|----|----|
| 1 | Open `https://HOST`. | Chat. The tab bar at the bottom shows Chat, Work, Apps, Library and More. Nothing runs off the right edge. |
| 2 | **Connect an account.** Apps → iCloud Calendar → Connect. Enter `recorded.user@icloud.com` and the password `zzzz-zzzz-zzzz-zzzz`, then tap Connect. | "Apple didn't accept that email and app-specific password. Nothing was saved." The password field is empty again. |
| 3 | Enter the same email with `abcd-efgh-ijkl-mnop`, then tap Connect. | The form closes. The account `recorded.user@icloud.com` shows with a green "Ready". |
| 4 | **Ask.** In Chat, type "What's on my Google calendar on 10 November?" and send. (Google is already connected on the local stack through recordings.) | A reply about 10 November, either in Chat or as a task under Work, with the result. The keyboard doesn't cover the message box. |
| 5 | Attach `contracts/assistant/recordings/samples/a19-duplicate-and-new.ics` (AirDrop it to the phone first). Tap the paperclip, pick it, and send "Preview importing this into my primary Google calendar." | The file chip shows upload progress, then the file name. The answer says Dentist is already there and Neighborhood potluck is new. |
| 6 | **Approve.** More → Automations → New automation. Fill in Name "Phone test", "What Nori does each time": "Add an event called Phone test to my iCloud calendar on <a date> at 9:00 for 30 minutes." Choose Once with any future date, tick the iCloud account, untick "Make changes it can undo", choose "Skip it", then tap Create automation. On the automation, tap Run now. | The task opens straight away. Within about a minute, an approval card appears: "Create the iCloud event 'Phone test' …". The header says "1 thing needs you" and the Work tab shows a badge. |
| 7 | Tap Approve. | The card goes away. The task finishes as Done: "Added Phone test to your iCloud Home calendar for …", with the create action Confirmed. |
| 8 | **Receive a notification.** More → Settings → Notifications. | Under Inbox: "Phone test", with the run's result and a time. "Mark seen" clears it. (Push to the phone's lock screen is not part of this run. It needs VAPID keys, and on iPhone it needs Nori added to the Home Screen.) |
| 9 | **Open the result.** Tap the inbox item, or Library → the newest item. | The saved result opens with its text, a version and the task it came from. "Open task" goes back to the task. |
| 10 | **Stop a task.** In Chat, send "Go through my Google calendar for each week of December and summarize every week." Then Work → the new task → Stop task, and confirm. | The task first shows "Stopping." and then "Stopped". It moves to Finished. Nothing else it started keeps running. |
| 11 | **Dictation** (needs HTTPS, which you have). On Chat, tap the mic and say a sentence. | Allow microphone access when asked. Words appear above the message box while you speak; when you stop, they land in the message box, and nothing is sent until you tap Send. |
| 12 | **Reconnect.** Turn on airplane mode, change a preference in Settings → Preferences, tap Save, then turn airplane mode off and tap Save again. | The first save says Nori couldn't confirm the change. The second applies it once, without a duplicate. |

## Screen reader (VoiceOver on iPhone, TalkBack on Android)

| # | Do | What you should hear |
|---|----|----|
| 13 | On Work, swipe through the page. | The heading "Work", then the tabs ("Needs you, 1", and so on), then each task. Every button says what it does. None are read as just "button". |
| 14 | Use the rotor's Landmarks (VoiceOver) to jump. | A navigation landmark for the tabs and a main landmark for the page. |
| 15 | Tap a task, then go Back. | Focus lands on the new page's heading. |
| 16 | Approve or answer something. | A spoken confirmation such as "Approved." without moving focus. |
| 17 | Open the Apps → iCloud form. | The fields are read as "Apple Account email" and "App-specific password". The link is read as "How to create one". |

Record any step that doesn't match, with the step number and a screenshot.
