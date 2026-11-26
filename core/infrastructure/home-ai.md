# Home AI Server

The connector runs beside a local Ollama instance. It uses outbound HTTPS and an authenticated WebSocket to a dedicated relay. Nori Core owns conversation history, model selection, tools, permissions, and the task queue.

## Rollout

1. Apply Core migrations on the target Postgres database through the normal migration job.
2. Create a relay service account and a Postgres login scoped to the home AI tables. Grant the relay SELECT on the nori_home_ai_accounts view so it can reject reset or missing accounts. Keep conversation, artifacts, and connected-app credentials outside the relay role. Use a dedicated service identity for Cloud SQL connectivity.
3. Create one shared 32-byte base64 encryption key in Secret Manager. Grant the Core and relay identities access. Supply individual Postgres URL secrets for Core and relay when their database roles differ.
4. Deploy the relay with [deploy-home-ai-relay.sh](../../scripts/deploy-home-ai-relay.sh) and point the public HTTPS pairing URL at the Nori Web `/connector/activate` page. Cap relay concurrency and instance count for the pilot. Apply per-source connection and OAuth endpoint limits at the public edge. Verify that the deployed ingress preserves client identity for rate limiting before broad enrollment.
5. Publish signed connector packages configured with the relay HTTPS origin. Serve versioned artifacts from the Nori release origin. Keep earlier supported builds available during the release window.
6. Enable Core by supplying `NORI_HOME_AI_KEY` and `NORI_HOME_AI_VERIFY_URL`. Configure the download URLs for clients after the artifacts are published. Confirm Core and relay readiness and pair a test account on staging.
7. Enable `NORI_HOME_AI_WORK_ENABLED=true` after confirming tool calls, tool results, and offline recovery against supported Ollama versions and models in staging.

| Core variables | Purpose |
| --- | --- |
| `NORI_HOME_AI_KEY` | Required base64 mailbox encryption key. Mount it as a secret. |
| `NORI_HOME_AI_VERIFY_URL` | First-party pairing page URL. Local development can use HTTP loopback. |
| `NORI_HOME_AI_POSTGRES_URL` | Optional connector-specific Postgres credentials on the same database. Defaults to `NORI_POSTGRES_URL`. |
| `NORI_HOME_AI_POSTGRES_LISTEN_URL` | Optional direct Postgres connection for notifications when the request pool uses transaction pooling. |
| `NORI_HOME_AI_DOWNLOAD_MACOS` | HTTPS URL for a signed, notarized Mac installer. |
| `NORI_HOME_AI_DOWNLOAD_LINUX_AMD64` | HTTPS URL for the amd64 Debian package. |
| `NORI_HOME_AI_DOWNLOAD_LINUX_ARM64` | HTTPS URL for the arm64 Debian package. |
| `NORI_HOME_AI_WORK_ENABLED` | Enables selection of home models with tool support for Work. Defaults to false. |

The relay uses the same key and verification URL, a Postgres URL, and `PORT`. Its pool defaults to eight connections per instance. Set `NORI_HOME_AI_MAX_CONNS` to change the limit. The deployment script accepts Secret Manager references in `NORI_HOME_AI_RELAY_DB_SECRET` and `NORI_HOME_AI_KEY_SECRET` in `name:version` format. Set `NORI_HOME_AI_RELAY_ACCOUNT` to the relay service account. Run the script with `--dry-run` to inspect build and deployment arguments.

Cloud Run treats a relay instance with an open WebSocket as active and billable. Measure idle connection cost and reconnect behavior with two staging replicas before raising the pilot limits. [Cloud Run WebSockets](https://docs.cloud.google.com/run/docs/triggering/websockets)

## Device setup

On the computer running Ollama, open **More → Models → Home AI Server → Get connector**. Open the Mac app or run `sudo nori-connector-control pair` on Linux. Enter the displayed code in **More → Models** and approve pairing, then save the device model for Chat. The Mac background helper starts at login. On Linux, systemd can start the service at boot.

Keep Ollama bound to loopback and configure it to start automatically. Set `disable_ollama_cloud` to `true` in the serving user's `~/.ollama/server.json` or set `OLLAMA_NO_CLOUD=1` in the service environment, then restart Ollama. [Ollama local-only configuration](https://docs.ollama.com/faq#how-do-i-disable-ollamas-cloud-features)

For a development build, use `nori-connector pair --url http://127.0.0.1:8092 --state-dir "$connector_state_dir"` and start `serve` with the same flags. The default Ollama endpoint is `http://127.0.0.1:11434`. Use the connector test in Nori to verify the full route.

## Recovery

- Revoking a device from Nori fences its connection and requests. Setting a replacement model is a separate choice.
- If the connection drops after the relay rotates a credential and before the connector saves its replacement, pair the connector again. The relay revokes the old credential family on replay.
- Chat displays connection and model failures with a retry when applicable. Home-routed Chat preserves the selected route.
- A Work model step waiting for an offline, busy, or unavailable home machine releases the worker lease and retries after a bounded wait. A disconnected device or changed model can require a new Work run with a current selection.
- Home-only mode blocks cloud inference at dispatch. Requests already in flight may finish. Background Chat memory consolidation is paused in this mode. Task tools still follow their configured permissions and integrations.
- Relay mailbox contents are encrypted and short-lived. Backups have their own retention. Nori Core still processes the prompt and reply.
- Turn off Work qualification separately. Coordinate disabling the Core feature with retiring the relay and revoke enrolled devices before taking the relay down.
