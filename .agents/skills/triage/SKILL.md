---
name: triage
description: >-
  Diagnose Nori production issues on GCP Cloud Run (bot-111549 / us-central1):
  inspect nori-core and nori-web revisions, read structured Cloud Logging traces,
  check env vars and model/provider configuration, and detect stuck worker pools
  or runaway LLM spend. Use when the user asks why prod is broken, asks to check
  prod logs, or suspects phantom LLM charges.
---

# Production Triage

Investigate live Nori production issues on GCP Cloud Run (`project=bot-111549`, `region=us-central1`) and report findings in plain English.

## Steps

1. **Check active revisions and health**
   - Inspect `nori-core` and `nori-web` Cloud Run services (`gcloud run services describe ...`) to see the deployed image SHA, latest ready revision, traffic split, and compare against `origin/main`.
   - Curl `nori-core`'s `/readyz` endpoint to verify readiness.

2. **Read recent structured logs**
   - Query Cloud Logging for `resource.type="cloud_run_revision"` and `resource.labels.service_name="nori-core"` (or `nori-web`), focusing on `ERROR` and `WARN` entries.
   - When an error has a `trace_id`, pull all log entries for that `trace_id` to trace the full request lifecycle.

3. **Inspect runtime config and spend safety**
   - Check `nori-core` env vars (`NORI_MODEL_PROVIDER`, `NORI_OUTER_PROVIDER`, `NORI_INNER_PROVIDER`, `NORI_AGENT_MODEL`, `MODEL_NAME`, `NORI_OUTER_REASONING_EFFORT`, `NORI_INNER_REASONING_EFFORT`, `NORI_LLM_MAX_USD`) for misconfigurations or unsupported parameter combinations.
   - Check for stuck background tasks, retry loops, or unexpected `model.call.completed` bursts that could cause phantom LLM charges.

4. **Report in plain English**
   - State the root cause clearly in 2–3 sentences without jargon walls.
   - Propose the concrete fix (config change, pool reset, or code patch + redeploy) and wait for confirmation before mutating production state unless the user already authorized it.
