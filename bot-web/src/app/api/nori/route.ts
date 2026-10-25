import Anthropic from "@anthropic-ai/sdk";
import { NORI_SYSTEM } from "@/lib/nori/prompt";
import type { ReplyRequest } from "@/lib/nori/backend";

const MODEL = "claude-sonnet-5-5";

function describe(err: unknown) {
  if (err instanceof Anthropic.AuthenticationError) return "The Anthropic API key in .env was rejected.";
  if (err instanceof Anthropic.RateLimitError) return "Nori is rate limited. Try again in a moment.";
  if (err instanceof Anthropic.BadRequestError) return `Bad request: ${err.message}`;
  if (err instanceof Anthropic.APIError) return `Claude API error ${err.status ?? ""}`.trim();
  return err instanceof Error ? err.message : "Couldn't reach Nori.";
}

const LOCAL_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]"]);

/** This route spends your API key, so only the app on this machine may call it. */
function isLocal(req: Request) {
  const host = req.headers.get("host")?.replace(/:\d+$/, "") ?? "";
  const origin = req.headers.get("origin");
  let originHost = host;
  if (origin) {
    try {
      originHost = new URL(origin).hostname;
    } catch {
      return false;
    }
  }
  return LOCAL_HOSTS.has(host) && LOCAL_HOSTS.has(originHost);
}

export async function POST(req: Request) {
  if (!isLocal(req)) return Response.json({ error: "Forbidden" }, { status: 403 });
  if (!process.env.ANTHROPIC_API_KEY) {
    return Response.json({ error: "Add ANTHROPIC_API_KEY to bot-web/.env, then restart the dev server." }, { status: 503 });
  }

  let payload: ReplyRequest;
  try {
    payload = (await req.json()) as ReplyRequest;
  } catch {
    return Response.json({ error: "Request body must be JSON." }, { status: 400 });
  }
  const { messages, context } = payload;
  if (!Array.isArray(messages) || messages.length === 0 || typeof context !== "string") {
    return Response.json({ error: "No messages to answer." }, { status: 400 });
  }

  const client = new Anthropic();
  const stream = client.beta.messages.stream(
    {
      model: MODEL,
      max_tokens: 16000,
      output_config: { effort: "medium" },
      // Policy declines retry on Anthropic's recommended fallback model.
      betas: ["server-side-fallback-2026-07-01"],
      fallbacks: "default",
      system: [
        { type: "text", text: NORI_SYSTEM, cache_control: { type: "ephemeral" } },
        { type: "text", text: context },
      ],
      messages,
    },
    { signal: req.signal },
  );

  const encoder = new TextEncoder();
  const body = new ReadableStream({
    async start(controller) {
      const send = (event: object) => controller.enqueue(encoder.encode(`${JSON.stringify(event)}\n`));
      try {
        for await (const event of stream) {
          if (event.type === "content_block_delta" && event.delta.type === "text_delta") {
            send({ t: "delta", text: event.delta.text });
          }
        }
        const final = await stream.finalMessage();
        if (final.stop_reason === "refusal") send({ t: "refusal" });
        else send({ t: "done", stop: final.stop_reason });
      } catch (err) {
        if (!req.signal.aborted) send({ t: "error", message: describe(err) });
      } finally {
        controller.close();
      }
    },
    cancel() {
      stream.abort();
    },
  });

  return new Response(body, {
    headers: { "Content-Type": "application/x-ndjson; charset=utf-8", "Cache-Control": "no-store" },
  });
}
