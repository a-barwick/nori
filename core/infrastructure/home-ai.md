# Home AI Server

The connector runs beside a local Ollama instance. It uses outbound HTTPS and an authenticated WebSocket to a dedicated relay. Nori Core owns conversation history, model selection, tools, permissions, and the task queue.

## Rollout

1. Choose the Mac Web origin and the protected relay hostname before building the Apple-silicon Nori for Mac DMG (`macos/`) and Debian connector packages (`connector/linux/`). Use the **Build Nori Mac and Home AI installers** workflow at the same Git revision and version. Check the Developer ID signature, notarization ticket, and signed Linux checksums. GitHub Releases can host the versioned downloads. In Models, the Mac download points to the Nori for Mac app (`macos/`), which bundles the connector helper and LaunchAgent.
2. Use [deploy-gcp-mac-web.sh](../../scripts/deploy-gcp-mac-web.sh) to create the Mac Web entry point for the dedicated Google Desktop OAuth client. Verify each pilot member’s Google identity and use the subject (`sub`) from the verified token. Store a JSON object that maps each Google subject to its lowercase email address in Secret Manager, with one subject per email. Set `NORI_MAC_GOOGLE_IDENTITIES_SECRET` to the secret name and numeric version when deploying. Mac sign-in requires that exact subject and email pair and a current, direct, unconditional IAP user grant for the email. Remove the IAP grant to revoke access within 30 seconds. Deploy a new secret version to update the pilot list. Before promotion, confirm that each listed pilot member is eligible based on direct, unconditional grants and that their effective IAP access does not depend on inherited deny rules or context-aware access controls. Regular Web continues to use IAP. After the service exists, the Release workflow updates it with `nori-web`.
3. Apply Core migrations on the target Postgres database before starting the relay. Use the normal migration job in a split deployment. For a combined deployment, run the release's Core image with `--migrate-only` as a Cloud Run job configured with the same database connection and identity as Core. Create a relay service account and a Postgres login scoped to the Home AI tables. Grant the relay SELECT on the `nori_home_ai_accounts` view so it can reject accounts that are missing, resetting, or being deleted. Keep conversation, artifact, and integration credentials outside the relay role.
4. Create one shared 32-byte base64 encryption key in Secret Manager and grant the Core and relay identities access. Use separate Postgres URL secrets when their database roles differ.
5. Set up an external HTTPS Application Load Balancer with a serverless NEG targeting the relay. Attach a Cloud Armor backend policy with a non-preview, per-IP throttle against all paths and a 429 response when the limit is reached. Use the protected edge as `NORI_HOME_AI_RELAY_PUBLIC_ORIGIN` and specify its backend in `NORI_HOME_AI_RELAY_EDGE_BACKEND`. The [relay deploy script](../../scripts/deploy-home-ai-relay.sh) checks the backend and policy, verifies the candidate with `/readyz` at startup, and restricts ingress to the load balancer. Embed the load balancer origin in all installers. Configure the pairing URL to the browser Web origin’s `/connector/activate` page. [Cloud Run ingress](https://docs.cloud.google.com/run/docs/securing/ingress)
6. Verify the protected relay returns HTTP 200 from `/readyz`, then publish the signed release and set the download URLs. Use [configure-core.sh](../../scripts/configure-core.sh) with `NORI_HOME_AI_KEY_SECRET` and the verification and download URLs. Verify authenticated Web in the browser. Enable Work after validating tool calls, tool results, and offline recovery with the supported Ollama model.
7. Check the signed installer with a fresh pilot account. Confirm sign-in, setup, iPhone Chat and scheduled Work while Nori for Mac is quit, relay recovery, disconnect, and switching accounts. The [macOS release guide](../../macos/README.md) covers the app build and Apple signing.

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

The relay uses the same key and verification URL, a Postgres URL, and `PORT`. The Cloud Run deploy script defaults to one relay instance at the service and revision levels, with four pooled database connections and one listener per instance. Check the Cloud SQL connection limit before increasing `NORI_HOME_AI_RELAY_MAX_INSTANCES` or `NORI_HOME_AI_RELAY_PG_MAX_CONNS`. Reserve capacity for Core’s separate Home AI pool, notification listeners, and tagged or draining revisions. The deployment script accepts Secret Manager references in `NORI_HOME_AI_RELAY_DB_SECRET` and `NORI_HOME_AI_KEY_SECRET` in `name:version` format. Set `NORI_HOME_AI_RELAY_ACCOUNT` to the relay service account. Run the script with `--dry-run` to inspect build and deployment arguments.

Cloud Run treats a relay instance with an open WebSocket as active and billable. Measure idle connection cost and reconnect behavior before raising the pilot limits. [Cloud Run WebSockets](https://docs.cloud.google.com/run/docs/triggering/websockets)

## Device setup

In Nori for Mac, open **More → Models → Use this Mac as a Home AI Server**. Select an installed Ollama model and approve background access. During setup, choose whether to use that model for Chat, Work, or both. Change either route later in **More → Models**. The per-user helper keeps running after you quit the window and starts at login while sharing is enabled. Signing out or switching accounts stops local sharing and revokes the Mac. If the relay is unreachable, the app retries revocation while open and on the next launch.

On a headless Debian or Ubuntu host, install the standalone package and run `sudo nori-connector-control pair`. Enter its code in **More → Models** from another signed-in device. systemd can start the service at boot.

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
