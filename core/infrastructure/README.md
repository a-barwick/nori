# Infrastructure

Nori targets **GCP + Postgres + GCS** (`NORI_CLOUD=gcp`) or **AWS + Postgres + S3** (`NORI_CLOUD=aws`) in production (`cmd/nori --role=all|api|worker`) and **Docker Compose (Postgres only)** locally (`NORI_CLOUD=local`).

- [local/README.md](local/README.md) — local Postgres Compose stack (`make deps`, `make dev`).
- [gcs/README.md](gcs/README.md) — native GCP runtime adapters (GCS JSON API via Workload Identity / ADC, Cloud KMS, bucket lifecycle policy, and `nori-storage-audit`).
- [aws/README.md](aws/README.md) — AWS runtime adapters (S3 artifacts, Secrets Manager, OIDC auth, HS256 service auth, unified `cmd/nori --role=all|api|worker`).
- [Dockerfile](Dockerfile) — digest-pinned distroless static Go image for `cmd/nori`.
- [`infra/`](../../infra) — Terraform for every GCP resource Nori runs on.

## Terraform

`infra/` describes the GCP project `bot-111549`: Cloud Run services and jobs, Cloud SQL, the artifact and state buckets, Artifact Registry, Cloud KMS, Secret Manager secrets, service accounts and their roles, Workload Identity Federation for GitHub, domain mappings, and the Home AI load balancer with its Cloud Armor policy. State is in `gs://bot-111549-nori-tfstate`.

| File | Contents |
| --- | --- |
| `run.tf` | Cloud Run services, jobs, invoker and IAP grants, domain mappings, and every environment variable and secret mount |
| `iam.tf` | Service accounts, project roles and Workload Identity Federation |
| `secrets.tf` | Secrets and which accounts can read them |
| `storage.tf` | Artifact and state buckets, Artifact Registry and Cloud KMS |
| `database.tf` | The `nori-pg` Cloud SQL instance, its backups and maintenance window |
| `home_ai_edge.tf` | Load balancer and Cloud Armor policy for `relay.noribot.chat` |
| `check-unmanaged.sh` | Lists project resources that `infra/` doesn't manage |

To change infrastructure, edit `infra/`, run `make infra-plan` with your own `gcloud` credentials, and merge to `main`. The **Infrastructure** workflow applies the plan as `nori-ci-infra`. It also runs every day and fails in either of these cases:

- A managed resource differs from `infra/`, such as an env var changed with `gcloud`.
- `check-unmanaged.sh` finds a resource that `infra/` doesn't manage, such as a service created with `gcloud run deploy`.

Its log lists the resources that change, without the diff, because the repository is public. `make infra-plan` runs both checks locally.

| Change | Where |
| --- | --- |
| An environment variable on a service | `local.env` in `run.tf`. Applying starts a new revision with the live image. |
| A secret value | `gcloud secrets versions add <name> --data-file=-`. A service that pins a numeric version, such as `nori-home-ai-key:1`, needs the new number in `run.tf`. |
| The allowed emails or Google tester list | The `nori-terraform-private` secret, a JSON object with `allowed_emails` and `google_workspace_test_users`. Add a version, then run the Infrastructure workflow. An empty tester list offers Google to every user. |
| Who can sign in through IAP | `gcloud iap web add-iam-policy-binding` on `nori-web`. People's grants stay outside Terraform. |

`nori-pg` takes a backup every day at 09:00 UTC and keeps seven, and Google applies maintenance on Sundays from 10:00 UTC. Deletion protection is on in GCP, and backups are kept if the instance is deleted. Point-in-time recovery is off, because turning it on restarts the instance. To restore, run `gcloud sql backups list --instance=nori-pg`, then `gcloud sql backups restore <id> --backup-instance=nori-pg --restore-instance=<target instance>`.

Terraform ignores each service's image, its `nori-commit` and `nori-prev` labels and its traffic, which releases own. Secret values, database users and OAuth clients are also outside Terraform.

## Cloud Run release

After CI's checks pass on a `main` commit, the **Build images** job pushes `nori-core:<sha>` and `nori-web:<sha>` to Artifact Registry. When `site/Dockerfile` exists, the same job then pushes `nori-site:<sha>` in a separate step, so a failed site build leaves the core and web images in place. The **Release** workflow runs every three hours. It also runs when someone presses **Run workflow** on the Release workflow's Actions page, or runs `gh workflow run release.yml`. The workflow runs [`scripts/release.sh --latest-green`](../../scripts/release.sh), which picks the newest `main` commit with passing checks and built core and web images. A missing `nori-site` image doesn't stop core or web from releasing. GitHub authenticates to GCP through Workload Identity Federation (pool `github`), which accepts only `a-barwick/nori` on `main`.

| Step | What happens |
| --- | --- |
| Choose services | Each revision carries a `nori-commit` label. A change under `core/` or `contracts/` releases the migration job, `nori-core` and `nori-worker`. A change under `web/` or `contracts/` releases `nori-web` and `nori-web-mac`, which share one image. A change under `site/` releases `nori-site`. The release skips `nori-site` when the commit has no `nori-site` image. |
| Stage | Each service gets a new revision with no traffic. The revision must pass its HTTP startup probe (`/readyz`, `/api/health` or `/healthz`). `nori-core` must also return 200 from `/readyz` on its tagged URL, `nori-web-mac` must return 403 to anonymous requests, and `nori-site` must return 200 from `/` and `/privacy` to anonymous requests. Cloud Run reserves `/healthz` on public URLs, so only the startup probe uses it. |
| Promote | Each staged revision receives all traffic. |
| Watch | For 10 minutes, a 5xx or container crash from a new revision moves its services back to the revisions named in their `nori-prev` labels. |

Migrations may only add schema, and `TestMigrationsOnlyAddSchema` fails on a drop or rename. A reverted revision therefore runs against a schema it supports. Removals and renames ship later, in a `*_cleanup.sql` migration.

| Command | Effect |
| --- | --- |
| `scripts/release.sh --dry-run` | Prints which services would ship. It refuses a commit that isn't on `origin/main` or whose CI checks haven't passed. |
| `scripts/release.sh --force-unverified` | Skips the CI check. |
| `scripts/release.sh --core`, `--web` or `--site` | Releases that group even if its code is unchanged. `--site` fails if the commit has no `nori-site` image. |
| `scripts/release.sh rollback core\|web\|site\|all` | Moves each service in the group back to its previous revision. |
| Repository variable `NORI_AUTO_RELEASE=false` | Stops the scheduled releases. **Run workflow** still releases. |

For the release that introduces issuer-and-subject account binding, Web must ship first. Merge and release the Web change with `scripts/release.sh --web`, then merge the Core change. Keep the gap short, because the Web services bypass their account-resolution caches until Core confirms the binding. After the Core migration, bind existing users that require operator verification using [`nori-user bind-identity`](../README.md#users). Verify sign-in with a provisioned account on each deployed client.

The Release workflow ships `nori-web-mac` with the `nori-web` image. [`scripts/deploy-home-ai-relay.sh`](../../scripts/deploy-home-ai-relay.sh) ships the Home AI relay image, and `docs/deploy.sh` builds the docs image. No workflow deploys the docs image.

The public site runs as `nori-site`, defined in `infra/run.tf`. It allows unauthenticated requests, has no IAP and runs as `nori-site@bot-111549.iam.gserviceaccount.com`, which has no project roles. Releases skip the site group until the service exists.
