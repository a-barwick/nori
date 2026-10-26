# Nova / Nori redesign POC

Visual reference: the product mockups on slides 5, 7, 8, 10, and 11 of
`Nori - The assistant inside Nova redesign.pdf`. The deck's business proposals
and operational instructions are not part of this implementation.

## Open the preview

From `web/`, install the lockfile dependencies and run:

```sh
bun install --frozen-lockfile
bun run dev --port 3211
```

Visit `http://127.0.0.1:3211/redesign` for the interactive concept, or `/` for
the redesigned live chat. The existing loopback development access and production
IAP checks apply to both routes.

The concept has three scenes: meeting preparation, a recap email and Salesforce
approval, and a renewals dashboard. Open the meeting rows, edit and save the recap,
simulate sending and approving, or open the dashboard. The composer adds messages
locally and recognizes meeting, follow-up, and dashboard prompts. Reset returns to
the initial state.

All concept data is fictional and all interactions are in memory. Sending a recap
does not send email; approving does not update Salesforce. Refreshing resets the
preview. No backend, live integrations, or deployment are needed for this route.

## Implementation

- `NovaShell` shares the white app frame, Nova branding, Nori identity, and mobile
  header between the live app and concept.
- Global semantic colors match the deck: white, plum ink, saturated purple,
  lavender borders and badges, and coral risk indicators. The POC stays light
  regardless of system theme, matching the reference.
- Live chat retains its runtime contract, task controls, history, document viewer,
  and disabled/offline states. Activity, refresh, and debug controls are in the header.
- The `/redesign` fixtures are separate from the live conversation and never
  replace runtime results. Their actions do not call APIs.
- Dialogs use the existing Radix dependency for keyboard focus, Escape, and dismissal.

## Validation

Production build, TypeScript, lint, and browser inspection at desktop and mobile
sizes. No tests were added. Live task execution requires the existing runtime and
was not exercised for this visual POC.
