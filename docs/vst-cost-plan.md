# VST cost reduction plan

## Where the money goes

Measured on `main` @ `8be513e`, 2026-11-24, from per-call `model_usage` records (Marcus, Tariq, Jordan; near-complete persona-months).

| Component | Model / route | Calls per persona | Cost per persona | Share |
|---|---|---:|---:|---:|
| **Outer conversation** | `gpt-6.1-sol`, OpenAI direct | 62–71 (~14–16k input tokens each, ~1.0M per month) | **$1.86–2.20** | ~75% |
| Task execution | `gpt-6.1-terra`, OpenRouter | 37–71 | $0.29–0.64 | ~15% |
| Memory flush / extraction | `gpt-6.1-terra`, OpenRouter | 38–45 | $0.09–0.21 | ~5% |
| AutoDream | `gpt-6.1-terra`, OpenRouter | 2–3 | not recorded | — |
| Judge + responder | `gpt-4.1` / `gpt-4.1-mini` | ~13 + ~10 | ~$0.1–0.2 (est.) | ~5% |
| **Total** | | | **≈ $2.5–3.1 per persona** | |

At that rate the full 10-persona suite costs **≈ $27**, about $6–7 of it on OpenRouter.

**The outer model has 0 cached input tokens on every call.**

- `SolCost` (`core/internal/usage/openai.go:17`) prices uncached input at $2.00/M and cached input at $0.10/M.
- Marcus's $2.20 is exactly 1,002,774 uncached input tokens plus 19,438 output tokens.
- `requestBody` (`core/internal/conversation/openai.go:200`) sends `prompt_cache_options: {"mode": "explicit"}`, but nothing in `core/` marks a cache breakpoint. So each turn re-pays for the same large static prefix (system prompt, tool schemas, Core Memory).
- **This is a product cost bug as well as a sim cost:** production pays it on every turn.

Separately, the OpenRouter **workspace** has a $50/month budget. It was exhausted on 2026-11-24 (it resets 2026-12-01). Every inner-model call then returns 403, which is invisible to `/api/v1/credits`. `suite.py` now detects this in its preflight.

## Plan, in order of savings per effort

### 1. Fix outer prompt caching (product) — DONE (2026-11-24)

**What changed:**

- **Four explicit cache breakpoints** on every reply call: the end of the static system prompt, the end of the memory/summary block, and the ends of the last two history turns. `buildMessages` returns them; `openAIConversation` emits them as `prompt_cache_breakpoint` content parts.
- **A stable reply schema.** Per-turn memory/task-ID enums in `response_format` were removed, because they invalidated the whole cache whenever a memory or task appeared (verified: one new memory ID → 0 cached tokens). Those IDs are now enforced after decoding by `validateReplyIDs`. Command-plan and work-request turns keep their enum-restricted schemas.
- **Cost accounting** now bills cache writes at 1.25× (`usage.SolCostWithWrites`). `model_usage` records `cached_tokens` / `cache_write_tokens`.
- **Tests:** unit tests in `core/internal/conversation/prompt_cache_test.go`, plus an opt-in live test, `NORI_LIVE_CACHE_TEST=1 go test ./internal/conversation -run TestLivePromptCacheHits`.

**Measured live through the real request path** (~8k-token prompt):

| Turn | Cached / input | Cost | Uncached today |
|---|---:|---:|---:|
| Cold | 0 / 7,632 | $0.0201 | ~$0.016 |
| History grew | 7,590 / 7,887 | **$0.0025** | ~$0.016 |
| New Core Memory | 3,370 / 8,195 | $0.0136 | ~$0.016 |
| History grew | 8,153 / 8,450 | **$0.0025** | ~$0.016 |

Steady-state turns are about 85% cheaper. Turns right after a Core Memory or summary change save less, because everything after the memory block is re-written. Expect roughly 40–60% off the outer model in the sim; measure it with the next suite run, since `model_usage` now carries the cache counts.

**Follow-up:**

- Moving Core Memory and the summary *after* the history would let memory changes keep the history cached. That is a prompt-structure change, so validate it with the suite first.
- The command-plan call is not cached: its static prompt is under the 1,024-token minimum and its context precedes the user message.

**Original estimate:** −50–65% of outer cost, ≈ $10–13 off a full suite, and the same ratio in production.

- Either mark a cache breakpoint after the static prefix (system + tools + Core Memory), or drop `mode: "explicit"` if automatic caching applies to this model.
- Order messages static → dynamic so the cacheable prefix is stable across turns.
- Verify by checking that `model_usage.cached_tokens` (or the span attribute `gen_ai.usage.cache_read.input_tokens`) becomes non-zero, then compare suite cost before and after.
- Check whether the inner model through OpenRouter caches too: task calls are 6–7k tokens with repeated tool schemas.

### 2. Tiered and targeted suites (harness)

**Most runs go from ≈ $27 to ≈ $3–8.**

| Tier | When | Personas | Est. cost |
|---|---|---|---:|
| `smoke` | every change | 01 Maya (or a 12-event `--max-events` slice) | ~$3 (full month) / <$1 (slice) |
| `targeted` | feature work | personas covering the touched feature area, per the matrix in `docs/vst-findings.md` §3 (e.g. memory → the existing `memory-pack` preset 01/03/05 plus 09 for Unicode FTS; permissions/approvals → 02/04/08; uploads → 01/03/07/10) | ~$8 |
| `full` | release candidates, weekly | all 10 | ~$27 now, ~$12–15 after #1 |

- Add a `--area <name>` option to `suite.py` that selects personas from a small area → personas map.
- Diff each run against the last full baseline for its personas only. `grader --compare` already skips missing slugs.

### 3. Cost visibility and a hard cap (harness)

- Record per-persona cost in `summary.json`. The runner already reads `/_sim/users/{id}/state`; add the `model_usage` sum by provider. `suite.py` can then print a cost table and abort when the run exceeds `--max-usd`.
- Before the drop, sum OpenRouter usage from `/api/v1/credits` deltas; OpenAI has no balance API, so use the `model_usage` totals.

### 4. Checkpoint and resume (harness, later)

- `pg_dump` each persona's DB at the weekly `settle` snapshots, and add `runner.py --resume-from <dump> --from-event <id>`.
- A change that only affects late-month behaviour (EOM synthesis, AutoDream) can then re-run days 22–30 for about 30% of the cost.

### 5. Small trims (product / harness)

- **Extraction cadence:** about 40 extraction/flush calls per persona, because the 5-minute idle trigger fires after nearly every sim event. Batch it, or lower its reasoning effort. Roughly $0.1/persona.
- **Judge:** use `gpt-4.1-mini` for per-probe grading and keep `gpt-4.1` for the end-of-month audit. A few cents per persona, but it adds up across many runs.

### Not recommended

**Swapping the outer or inner models for cheaper ones in "real" baselines.** That tests a different product. A cheap-model tier is fine for exercising the harness itself, but `OpenAIModel.requestBody` currently rejects non-default direct models (`openai.go:188`), so it would need a sim-only override.

## Suggested next steps

1. Fix outer caching (#1) and measure on a single `smoke` persona, about $3.
2. Add `--area` and cost reporting to `suite.py` (#2, #3).
3. Raise the OpenRouter workspace budget, or wait for the 2026-12-01 reset. Then run the full suite once on `main` to set the new baseline.
