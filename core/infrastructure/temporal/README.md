# Temporal task workers

Nori runs one pinned Temporal workflow per task. Ordinary Go model/computer
functions execute in bounded activities on versioned Cloud Run worker pools.
Firestore stores task state, commands and receipts; Cloud Storage stores large
artifacts. Temporal Cloud owns durable waiting and sequencing.

The live namespace is `quickstart-nori.qtjow`, deployment `nori-task`, queue
`nori-tasks`, with pools in `us-west1`. Terraform uses the existing external state
at `~/.local/state/nori/bot-111549/temporal/terraform.tfstate`.

## Scaling and pause

Worker versions use min **0**, max **1**, initial **0**, utilization **0.8** and
scale-down stabilization **90s**. Pass all five scaling flags together. These
bounds apply per retained version; overlapping pinned versions can each need a
pool. Keep old pools and immutable images while their pinned workflows remain.

`scalable_build_ids` records which retained builds support pause-only control.
The release helper adds each new compatible build; its default is empty. The
legacy `poc-*` workers enforce expiry and can cancel other tasks, so their images
and pools remain retained at zero with no scaler configuration or WCI pool grant.
Unpause refuses a Current/ramping build outside this set, or an open workflow
pinned to an incompatible or unknown build. Such histories need an explicit
migration decision; unpause never moves or cancels them.

IAM is stable and scoped to each compatible pool. There are no execution sessions,
admission expiry, expiring IAM grants, disabled invoker, or scheduled worker
shutdowns. A normal task starts without an operator resume step.

From `core/`, the explicit cost-control switch is:

```sh
python3 infrastructure/poc/poc.py pause
python3 infrastructure/poc/poc.py unpause
```

The helper reads `TEMPORAL_API_KEY` from the existing root `.env` without logging
it. `NORI_ENV_FILE` can select the existing project file when operating from a
worktree. No credentials are copied into the worktree or Terraform.

Pause closes application admission, removes each retained version's native
compute configuration with `temporal worker deployment
update-version-compute-config --remove`, and reconciles every pool to zero.
It stops the retained computer and preserves waiting tasks. Removing the scaler
configuration fences WCI before the zero-count update; merely setting a pool to
zero would allow the controller to start it again. A partial failure is reported
as incomplete pause. Explicit Stop remains a separate task operation.

Unpause restores compatible native compute configurations with the bounds above, opens
admission, and asks the runtime to deliver pending commands and wake existing
workflows. It does not create a replacement task or start the computer. The next
computer tool starts the retained VM on demand.

## Immutable releases

`terraform/worker-releases.json` retains every deployed build ID and image digest,
the compatible build set, and the enabled billing-alert configuration. Pass this
complete file to every plan so future releases preserve those resources.
Never change an image behind a live build ID. New pools begin at zero. Always
plan with the complete release manifest; omitting it selects an empty default,
which deletion protection rejects for existing pools. Preserve the compatible
build set alongside the image map.

Prepare a local image archive and an exact Terraform plan from `core/`:

```sh
python3 infrastructure/temporal/worker-release.py --build-id BUILD_ID
```

This requires a running OCI-capable Docker Buildx builder. If the default driver
does not export OCI, create a local `docker-container` builder and select it with
`--builder NAME`. It exports a single-platform OCI
archive without pushing it, records the immutable image digest and source
fingerprint, preserves all old manifest entries, and saves `release.json`, the
candidate manifest, `release.plan`, and `plan.txt` in an owner-only directory
beside the external state. Preparation fails if sources change during the build. Provenance records both
the base commit and whether the working tree is dirty; the source fingerprint
identifies the actual worker inputs. Each build has an immutable image digest,
without claiming byte-identical rebuilds across changing base tags.
When optional Terraform features are configured, include their settings with
`--var-file PATH` so preparation preserves them in the same plan.
An already built registry digest can instead be supplied with `--image URI`;
the operator must ensure it corresponds to the intended source.

Review `plan.txt` before any publishing or infrastructure change. The prepared
image must be published preserving the recorded OCI digest before applying the
saved plan; an OCI-aware registry tool such as `skopeo copy --preserve-digests`
can publish the exact archive. Publishing, applying, and promoting are separate
authorized operations. The helper never performs them.

After authorization, apply the reviewed plan and retain its candidate manifest
as the repository's release manifest. The manifest also retains
`runtime_invoke_root`, taken from the POC `runtime_url` output. Workers use this
invoke root and the existing conversation queue/caller to wake the outer loop
for durable task events. The worker shares `nori-poc-runtime`, so its scoped
queue enqueue and wake-caller `actAs` permissions remain owned by the POC
Terraform state; do not duplicate those IAM bindings here.

Register the new version with
`temporal worker deployment create-version`, its pool/provider identity and all
five scaling flags, then promote it with `set-current-version`. For existing
versions, use `update-version-compute-config`; `create-version` rejects an
existing build ID. Current routes new tasks; retained pinned tasks continue to
require their original versions. Registration alone does not promote a build.

**First migration from execution sessions:** run the new `pause` helper before
applying the stable-IAM Terraform change. Existing versions can still have
compute configurations while the old disabled account/expired IAM currently
blocks them. Remove those configurations before enabling stable scaler access.
Clear the old VM schedule in place with `poc.py clear-deadline` while stopped
(Google provider 8.8.1 would replace the instance when removing that field).
Unpause verifies the schedule is absent. Apply the runtime and computer changes, register the new worker version, then unpause
when authorized. Verify native bounds and zero pools before reporting paused.
Never recreate the retained disks, browser profile, or task files during this
migration.

## Credentials and measurements

The existing Secret Manager container `nori-temporal-api-key` supplies the server
credential to workers through a secret reference. Its payload must never enter
Terraform, state, images, plans, or logs. The root `.env` remains the local source.

Measure a representative real task from acceptance to first worker activity,
computer start to readiness, final activity to pool zero, and last tool activity
to VM termination. Record actual model token usage and estimated model USD
separately from observed compute duration and delayed provider billing. A local
smoke check cannot establish WCI scale-in, GCE stop/restart, or exact billed task
cost. Model, network, build, registry and retained storage charges are separate.

## Optional daily spend alert

`spend-alert.py --check` generates or reads the current UTC billing month's
Temporal account report, downloads the provider CSV, and reports whether the
configured threshold has been reached. The default threshold is $10 USD. It sums
all reported `ContractedCost` charge records across this account's namespaces
and charge categories; it does not include GCP costs or claim to be a finalized
invoice. [Temporal's billing schema](https://docs.temporal.io/cloud/billing-api)
labels USD values as cents, so the configurable default divides by 100. Current
month data is provisional and lags at least 24 hours, rounded to daily granularity.

Read the existing key in place for a local check (no metrics or email):

```sh
python3 core/infrastructure/temporal/spend-alert.py --check \
  --env-file /absolute/path/to/the/existing/root/.env
# Reuse a requested report instead of generating another overlapping report:
python3 core/infrastructure/temporal/spend-alert.py --check \
  --env-file /absolute/path/to/the/existing/root/.env --report-id REPORT_ID
```

The account-level `TEMPORAL_BILLING_API_KEY` requires billing-report access. The
namespace execution key is a different credential. No key or signed download URL
is logged or persisted. JSON output includes the report ID, generation/check
times, charge coverage when available, unit basis, total, and threshold. Empty
reports, unsupported currency, unparseable costs, and provider failures produce
unknown spend instead of $0; exit 2 means a generated report had no interpretable
charge total, while exit 1 means a request or processing failed. Only a complete
report returns exit 0. A nonempty report also checks cost against unit price and
quantity before conversion. The actual account report inspected during local
validation had headers and no charge rows; nonzero cents-to-USD conversion
therefore remains unverified against this account's real values.

`terraform/spend-alert.tf` creates nothing unless `spend_alert` is configured with
a reviewed immutable image digest and an **existing** Secret Manager billing-key
reference. It never creates or uploads a key payload. `Dockerfile.alert` builds
from `core/`; its default command is read-only `--check`. The deployed Job alone
passes `--publish`, using its own service account to write native Cloud Monitoring
metrics. Publication requires a newly generated report; `--report-id` remains
read-only inspection so an old report cannot become a fresh successful check. It has one task, parallelism one, no retries, a six-minute timeout, and
no resident capacity. Scheduler runs it once daily at 17:00 UTC by default; the
cron schedule, USD threshold, cost-unit override, and secret version are configurable.
It has no permission to wake task workers or the computer.

Native Monitoring emails Austin's configured GCP account address when the daily
report reaches the threshold or the report fails/is unknown. The threshold and failure conditions retain the preceding daily
point for up to 24 hours; a month reset or repaired check can therefore take a
day to clear. A Job that never runs or cannot publish any metric needs inspection in
Job/Scheduler diagnostics; deployment acceptance must verify the schedule, initial
metric, and email channel. No notification or deployment was performed during local validation.
This monitor adds normal Cloud Run Job, Scheduler, and Monitoring usage. It is an
alert, not a charge cap, and does not pause execution automatically.
