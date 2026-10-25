/**
 * The seam between the UI and whatever answers as Nori. Today it's a Next.js
 * route that calls Claude; later this is the core agent service API.
 */

import type Anthropic from "@anthropic-ai/sdk";

export type ApiMessage = Anthropic.Beta.Messages.BetaMessageParam;
export type ApiContent = Anthropic.Beta.Messages.BetaContentBlockParam;

export interface ReplyRequest {
  messages: ApiMessage[];
  context: string;
}

export class NoriUnavailable extends Error {}
export class NoriRefused extends Error {}

export interface NoriBackend {
  reply(req: ReplyRequest, signal?: AbortSignal): AsyncIterable<string>;
}

type Event = { t: "delta"; text: string } | { t: "done"; stop: string | null } | { t: "refusal" } | { t: "error"; message: string };

export const routeBackend: NoriBackend = {
  async *reply(req, signal) {
    const res = await fetch("/api/nori", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(req),
      signal,
    });
    if (!res.ok || !res.body) {
      const detail = await res.json().catch(() => ({ error: res.statusText }));
      throw new NoriUnavailable(detail.error ?? "Nori is unavailable");
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let nl: number;
      while ((nl = buffer.indexOf("\n")) >= 0) {
        const line = buffer.slice(0, nl).trim();
        buffer = buffer.slice(nl + 1);
        if (!line) continue;
        const event = JSON.parse(line) as Event;
        if (event.t === "delta") yield event.text;
        else if (event.t === "refusal") throw new NoriRefused("declined");
        else if (event.t === "error") throw new NoriUnavailable(event.message);
      }
    }
  },
};
