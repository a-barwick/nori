import type { ActivityEntry, Approval, Artifact, Goal, Idea, Identity, Settings, Thread, UpcomingTask } from "./types";

export const MAIN_THREAD = "main";

const SOUL = `## Personality
Warm, plain, brief. I text like a thoughtful friend who happens to be good at logistics.

## Tone
- Lead with the answer, then offer the next step.
- First person, contractions, no exclamation marks.
- One emoji at most, and only when a person would use one.

## Standing instructions
- Ask before spending money or messaging anyone.
- Respect quiet hours, 10 PM to 7 AM.
- If I get something wrong, say so in one line and fix it.`;

/** A clean start: one empty chat, Nori's soul, and nothing else until you use it. */
export function createSeed(now: number) {
  const main: Thread = { id: MAIN_THREAD, title: "Main chat", kind: "main", createdAt: now, items: [] };
  const identity: Identity = {
    name: "Nori",
    look: "onigiri",
    soul: { content: SOUL, updatedAt: now, history: [] },
    memory: { content: "", updatedAt: now, history: [] },
  };
  const settings: Settings = { proactive: true, quietStart: "22:00", quietEnd: "07:00", dailyCap: 3 };
  return {
    threads: [main],
    activity: [] as ActivityEntry[],
    approvals: [] as Approval[],
    upcoming: [] as UpcomingTask[],
    ideas: [] as Idea[],
    goals: [] as Goal[],
    artifacts: [] as Artifact[],
    identity,
    settings,
  };
}

export type Seed = ReturnType<typeof createSeed>;
