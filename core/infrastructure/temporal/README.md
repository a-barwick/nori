# Temporal worker infrastructure

Terraform creates Cloud Run worker pools in `us-west1` for the GCP-hosted `quickstart-nori.qtjow` namespace. It reuses the POC's private Artifact Registry, Firestore, artifact bucket, runtime service account, and retained computer. It does not manage or replace the existing POC Terraform state.

Initialize this module with its own local state outside the checkout:

```sh
terraform -chdir=core/infrastructure/temporal/terraform init \
  -backend-config=path="$HOME/.local/state/nori/bot-111549/temporal/terraform.tfstate"
```

`worker_images` maps an immutable, DNS-safe build ID to an image digest in the existing `us-central1` registry. Use a new build ID and pool for each code version; never change the image behind a live version. Keep old pools while Pinned Workflows reference them. Drain those Workflows before switching the sole active pool; retained old Pinned Workflows cannot resume while their pool has no WCI grant. All pools start at zero. Terraform ignores subsequent manual count changes because Temporal's WCI and the shutdown jobs own them.

The checked-in `terraform/worker-releases.json` records the retained image versions. Include `-var-file=worker-releases.json` on plans from that Terraform directory; omitting it selects the empty default and deletion protection rejects removing retained pools. Keep session settings separate and leave `wci_enabled=false` until shutdown is verified.

Without a release file, the default configuration creates no pools. It keeps the separate WCI invoker service account disabled, grants no WCI pool access, and keeps shutdown jobs paused. Secret Manager holds the worker's server credential; only its container and reference are tracked here. No secret payload belongs in Terraform, tfvars, state, an image, or deployment logs. Upload the root `.env` key through the native Secret Manager API only when deployment is authorized, then reference it as `TEMPORAL_API_KEY`. Do not inject plaintext into the pool environment: Terraform refresh would copy that value into state. Austin approved Secret Manager for this server-side credential.

## Before an attended session

1. Bound the hard deadline by the earlier of three hours from the authorized start and 22:00 America/Los_Angeles, using the existing POC deadline logic. Set application admission expiry early enough to cancel work and clean up.
2. Configure `session`: WCI `access_expires_at` at least two minutes before `shutdown_at`, and minute-aligned `shutdown_at` at least ten minutes before `hard_deadline_at`. All timestamps must be UTC. Terraform validates these margins and the three-hour limit; the operator must verify the Los Angeles cutoff.
3. With WCI still disabled, prove each shutdown job's OAuth identity can PATCH its pool to zero, observe successful reconciliation and no running instances, and verify its next scheduled run is the intended UTC time. Cloud Scheduler may execute late, and HTTP success acknowledges an asynchronous Cloud Run update. The margin and observed reconciliation matter. Existing POC VM shutdown must also be installed and verified.
4. Set `shutdown_verified=true` only after that live proof. Set exactly one `active_build_id`; then `wci_enabled=true` may enable its invoker and expiring IAM grants. Old retained pools get shutdown jobs but no WCI pool grant. Verify effective IAM, including inherited grants, before allowing task routing.
5. Register the corresponding Temporal Worker Deployment Version with min **0**, max **1**, initial **0**, utilization **0.8**, and stabilization **90s**; all five CLI scaling flags must be passed together. The worker uses deployment `nori-task`, queue `nori-tasks`, and its immutable build ID. Validate Connection checks impersonation/read access only. After the access and shutdown checks pass, promote the immutable build with `temporal worker deployment set-current-version --namespace quickstart-nori.qtjow --deployment-name nori-task --build-id <BUILD_ID>`. Registration alone does not route tasks or let WCI start instances; the version must be Current before an attended task proves scaling and actual execution.

The max of one is enforced by Temporal WCI version configuration, per version. Cloud Run MANUAL mode follows `manualInstanceCount`; native min/max fields do not impose a hard maximum on it, so Terraform omits those fields. This POC permits WCI to access only one pool at a time. There is no native aggregate cap, and a broad inherited grant or a manual count change could bypass the WCI restriction.

## Pause

Close durable session admission and cancel active work, then revoke WCI pool access and disable its invoker account. [Disabling the service account rejects existing access tokens while it remains disabled](https://docs.cloud.google.com/iam/docs/reference/rest/v1/projects.serviceAccounts/disable); they become usable again if the account is re-enabled. Remove or expire the effective pool IAM grant as an independent fence. PATCH **every** retained pool's `scaling.manualInstanceCount` to **0**, including old Pinned versions, and wait for reconciliation and no running instances. Stop the retained computer through the existing POC pause path. Verify effective WCI update denial and all zero counts before reporting paused; worker process exit alone leaves requested pool capacity billable.

After verification, set `wci_enabled=false` and `session=null` to leave jobs paused. The cron schedule names a UTC month/day/time and can recur annually if left enabled; it only shuts down, but must still be paused after the session. Never schedule a start or leave invocation access open overnight. Secret versions, images, retained disks, objects, and database data can incur charges after compute pauses.

Sources: [Temporal Cloud Run deployment](https://docs.temporal.io/production-deployment/worker-deployments/serverless-workers/cloud-run), [pool/version lifecycle](https://docs.temporal.io/serverless-workers/cloud-run), [Cloud Run manual scaling](https://docs.cloud.google.com/run/docs/configuring/workerpools/manual-scaling), [resource-level IAM](https://docs.cloud.google.com/run/docs/securing/managing-access).
