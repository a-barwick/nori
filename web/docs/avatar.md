# Nori avatar state protocol

Status: requirement, not built yet. It replaces the single global `markState` (`idle | attentive | thinking | writing | listening | pleased`) in `src/lib/store.ts`.

Callers send semantic signals about what is happening. They never choose or ship animations; the mark decides how each signal looks.

## Three layers

The avatar shows three independent layers at once.

### 1. Mode

A mode is sustained, and exactly one is shown at a time.

| Mode | When | Looks like |
| --- | --- | --- |
| `idle` | Nothing is happening | Breathes, blinks |
| `attentive` | You're typing | Leans in, eyes on the composer |
| `listening` | Voice input is on | Parked, listening |
| `thinking` | Your message is sent and no words have arrived yet | Eyes wander |
| `speaking` | A reply is streaming | Small bob |
| `working` | A tool call or long task is running | Busy, with optional progress from 0 to 1 |
| `waiting` | Nori needs you: an approval is pending or a question is open | Gentle pulse |
| `trouble` | Offline, an error, or a retry | Small frown, dimmed |
| `resting` | Quiet hours or away | Eyes closed |

### 2. Gesture

A gesture is a one-shot of 0.4 to 1 second, after which the mark returns to its mode.

`nod`, `pleased`, `celebrate`, `wave`, `perk`, `oops`, `shrug`, `blink`.

### 3. Gaze

Where the eyes point: `center`, `composer`, `panel`, `cursor`, or `element(id)`, with an optional `holdMs`.

## Signals

```ts
type AvatarSignal =
  | { kind: "mode"; mode: Mode; source: string; progress?: number; ttlMs?: number }
  | { kind: "release"; source: string }
  | { kind: "gesture"; name: Gesture; intensity?: 0 | 1 | 2 }
  | { kind: "gaze"; target: GazeTarget; holdMs?: number };
```

## Rules

- **Modes are claims, not overwrites.** Each source (`engine`, `composer`, `approvals`, `action:<id>`) claims a mode and later releases it. The highest-priority claim wins:
  `trouble` > `waiting` > `listening` > `speaking` > `thinking` > `working` > `attentive` > `idle` > `resting`.
- **Every claim expires.** A claim has a TTL (30 seconds by default) and its source must renew it, so a crashed action can't leave Nori stuck. This generalizes the `claimBusy` tokens in `src/lib/nori/engine.ts`.
- **Gestures are queued, not stacked.** A burst merges into one, there is at most one gesture per ~600 ms, and gestures are dropped while `resting`.
- **The vocabulary is fixed.** An unknown mode falls back to `idle`, and an unknown gesture plays nothing. Custom actions can't define their own animations.
- **Every look implements all of it.** Each look (onigiri, sprout, pebble, and any future one) implements every mode, gesture and gaze, plus a still pose for each state under reduced motion.
- **Only two marks are live.** The top medallion and the mark on the latest message animate; older inline marks stay still.

## Mapping from today

| Today | Under the protocol |
| --- | --- |
| `thinking`, then `writing` once a reply streams | `thinking`, then `speaking` |
| `attentive` while you type | the `composer` source claims `attentive`; gaze `composer` |
| eyes follow the pointer on the medallion | gaze `cursor` |
| `pleased` after a reply lands | `pleased` gesture |
| `listening` while recording | the `composer` source claims `listening` |
| A pending approval (not shown today) | the `approvals` source claims `waiting` |
| `liveError` (not shown today) | the `engine` source claims `trouble` |
| Quiet hours (not shown today) | `resting` |

## Implementation shape

The store gets an `avatar` slice holding a claims map (source → mode, progress, expiry) and a gesture queue. `NoriMark` reads the resolved mode, the current gesture and the gaze from it.
