"use client";

import Link from "next/link";
import { useAssistant } from "@/lib/assistant/context";
import { cadence, eventTime, relative } from "@/lib/assistant/format";
import { useResource } from "@/lib/assistant/useResource";
import type { AssistantFeature, Automation, CardPart, Connection, Goal, Interaction, LibraryItem, WorkDetail } from "@/lib/contracts/assistant";
import { connectionState } from "./AppsScreen";
import { InteractionCard } from "./InteractionCard";
import { provenanceLabel } from "./LibraryScreen";
import { statusPill } from "./WorkScreen";
import { Pill } from "./ui";

type Kind = CardPart["resource"]["kind"];
const ROUTES: Record<Kind, { path: (id: string) => string; feature: AssistantFeature; href: (id: string) => string }> = {
  interaction: { path: (id) => `/interactions/${id}`, feature: "interactions", href: () => "/work?tab=needs_you" },
  automation: { path: (id) => `/automations/${id}`, feature: "automations", href: (id) => `/routines/${id}` },
  goal: { path: (id) => `/goals/${id}`, feature: "goals", href: (id) => `/goals/${id}` },
  connection: { path: (id) => `/connections/${id}`, feature: "connections", href: () => "/apps" },
  task: { path: (id) => `/work/${id}`, feature: "work", href: (id) => `/work/${id}` },
  result: { path: (id) => `/library/${id}`, feature: "library", href: (id) => `/library/${id}` },
};

/** A typed card in a message. It always reads the current server state; the part never carries actions. */
export function CardView({ part }: { part: CardPart }) {
  const { has } = useAssistant();
  const route = ROUTES[part.resource.kind];
  const id = encodeURIComponent(part.resource.id);
  const enabled = !!route && has(route.feature);
  const { data, error, loading } = useResource<unknown>(enabled ? route.path(id) : null, { poll: part.kind === "progress" || part.resource.kind === "task" ? 5_000 : 30_000 });
  if (!enabled) return <p className="text-body">{part.fallback}</p>;
  if (loading) return <div className="h-16 animate-pulse rounded-card bg-fill-1 motion-reduce:animate-none" role="status" aria-label="Loading" />;
  if (!data) return <div className="rounded-card border border-dashed border-hairline-strong px-4 py-3 text-meta text-ink-2" role="note">{part.fallback}{error ? <span className="block text-caption text-ink-3">No longer available.</span> : null}</div>;
  const href = route.href(id);
  switch (part.resource.kind) {
    case "interaction": return <InteractionCard interaction={data as Interaction} />;
    case "task": {
      const { item } = data as WorkDetail;
      const pill = statusPill(item);
      return <Shell href={href} title={item.title} detail={item.error?.message ?? item.progress} pill={<Pill tone={pill.tone}>{pill.label}</Pill>} />;
    }
    case "automation": {
      const routine = data as Automation;
      return <Shell href={href} title={routine.title} detail={`${cadence(routine.trigger)}${routine.status === "enabled" && routine.next_at ? ` · next ${relative(routine.next_at)}` : ""}`} pill={<Pill tone={routine.status === "enabled" && (routine.next_at || routine.trigger.kind !== "once") ? "ok" : "neutral"}>{routine.status === "enabled" ? routine.next_at || routine.trigger.kind !== "once" ? "On" : "Done" : routine.status === "paused" ? "Paused" : "Cancelled"}</Pill>} />;
    }
    case "goal": {
      const goal = data as Goal;
      const met = goal.completion_criteria.filter((criterion) => criterion.met).length;
      return <Shell href={href} title={goal.title} detail={goal.blockers[0] ? `Blocked: ${goal.blockers[0].text}` : goal.progress} pill={<Pill tone={goal.status === "completed" ? "ok" : "accent"}>{met} of {goal.completion_criteria.length}</Pill>} />;
    }
    case "connection": {
      const connection = data as Connection;
      const state = connectionState(connection);
      return <Shell href="/apps" title={[connection.title, connection.account?.label].filter(Boolean).join(" · ")} detail={connection.health.message} pill={<Pill tone={state.tone}>{state.label}</Pill>} />;
    }
    case "result": {
      const item = data as LibraryItem;
      if (item.data_kind === "calendar_event" && item.data) return <Shell href={href} title={item.data.title} detail={`${eventTime(item.data.time)}${item.data.location ? ` · ${item.data.location}` : ""}`} pill={<Pill tone={item.data.status === "cancelled" ? "neutral" : "ok"}>{item.data.status === "cancelled" ? "Cancelled" : "On calendar"}</Pill>} />;
      if (item.data_kind === "mail_draft" && item.data) return <Shell href={href} title={item.data.subject || "Draft"} detail={`To ${item.data.to.map((to) => to.email).join(", ")}`} pill={<Pill tone={item.data.state === "sent" ? "ok" : "neutral"}>{item.data.state === "sent" ? "Sent" : "Draft"}</Pill>} />;
      return <Shell href={href} title={item.title} detail={provenanceLabel(item)} pill={item.version > 1 ? <Pill>v{item.version}</Pill> : undefined} />;
    }
  }
}

function Shell({ href, title, detail, pill }: { href: string; title: string; detail?: string; pill?: React.ReactNode }) {
  return <Link href={href} className="flex min-h-14 items-center gap-3 rounded-card border border-hairline bg-surface px-4 py-3 hover:border-hairline-strong hover:bg-fill-1">
    <span className="min-w-0 flex-1"><span className="block truncate text-row font-medium text-ink">{title}</span>{detail && <span className="block truncate text-meta text-ink-2">{detail}</span>}</span>
    {pill}
  </Link>;
}
