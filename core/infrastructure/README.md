# Infrastructure

Nori targets **GCP + Postgres + GCS** (`NORI_CLOUD=gcp`) or **AWS + Postgres + S3** (`NORI_CLOUD=aws`) in production (`cmd/nori --role=all|api|worker`) and **Docker Compose (Postgres only)** locally (`NORI_CLOUD=local`).

- [local/README.md](local/README.md) — local Postgres Compose stack (`make deps`, `make dev`).
- [gcs/README.md](gcs/README.md) — native GCP runtime adapters (GCS JSON API via Workload Identity / ADC, Cloud KMS, bucket lifecycle policy, and `nori-storage-audit`).
- [aws/README.md](aws/README.md) — AWS runtime adapters (S3 artifacts, Secrets Manager, OIDC auth, HS256 service auth, unified `cmd/nori --role=all|api|worker`).
- [Dockerfile](Dockerfile) — digest-pinned distroless static Go image for `cmd/nori`.

## Cloud Run release

After CI's checks pass on a `main` commit, the **Build images** job pushes `nori-core:<sha>` and `nori-web:<sha>` to Artifact Registry. The **Release** workflow runs every three hours. It also runs when someone presses **Run workflow** on the Release workflow's Actions page, or runs `gh workflow run release.yml`. The workflow runs [`scripts/release.sh --latest-green`](../../scripts/release.sh), which picks the newest `main` commit with passing checks and built images. GitHub authenticates to GCP through Workload Identity Federation (pool `github`), which accepts only `a-barwick/nori` on `main`.

| Step | What happens |
| --- | --- |
| Choose services | Each revision carries a `nori-commit` label. A change under `core/` or `contracts/` releases the migration job, `nori-core` and `nori-worker`. A change under `web/` or `contracts/` releases `nori-web` and `nori-web-mac`, which share one image. |
| Stage | Each service gets a new revision with no traffic. The revision must pass its HTTP startup probe (`/readyz` or `/api/health`). `nori-core` must also return 200 from `/readyz` on its tagged URL, and `nori-web-mac` must return 403 to anonymous requests. |
| Promote | Each staged revision receives all traffic. |
| Watch | For 10 minutes, a 5xx or container crash from a new revision moves its services back to the revisions named in their `nori-prev` labels. |

Migrations may only add schema, and `TestMigrationsOnlyAddSchema` fails on a drop or rename. A reverted revision therefore runs against a schema it supports. Removals and renames ship later, in a `*_cleanup.sql` migration.

| Command | Effect |
| --- | --- |
| `scripts/release.sh --dry-run` | Prints which services would ship. It refuses a commit that isn't on `origin/main` or whose CI checks haven't passed. |
| `scripts/release.sh --force-unverified` | Skips the CI check. |
| `scripts/release.sh --core` or `--web` | Releases that group even if its code is unchanged. |
| `scripts/release.sh rollback core\|web\|all` | Moves each service in the group back to its previous revision. |
| `scripts/configure-core.sh` | Changes optional Core settings: the Google Workspace beta, Home AI and the LISTEN URL secret. Releases keep existing settings. |
| Repository variable `NORI_AUTO_RELEASE=false` | Stops the scheduled releases. **Run workflow** still releases. |

For the release that introduces issuer-and-subject account binding, Web must ship first. Merge and release the Web change with `scripts/release.sh --web`, then merge the Core change. Keep the gap short, because the Web services bypass their account-resolution caches until Core confirms the binding. After the Core migration, bind existing users that require operator verification using [`nori-user bind-identity`](../README.md#users). Verify sign-in with a provisioned account on each deployed client.

[`scripts/deploy-gcp-mac-web.sh`](../../scripts/deploy-gcp-mac-web.sh) creates the `nori-web-mac` service, its service account and its IAP policy access. After the service exists, the Release workflow updates it with `nori-web`.
