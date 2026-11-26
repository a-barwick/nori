# Infrastructure

Nori targets **GCP + Postgres + GCS** (`NORI_CLOUD=gcp`) or **AWS + Postgres + S3** (`NORI_CLOUD=aws`) in production (`cmd/nori --role=all|api|worker`) and **Docker Compose (Postgres only)** locally (`NORI_CLOUD=local`).

- [local/README.md](local/README.md) — local Postgres Compose stack (`make deps`, `make dev`).
- [gcs/README.md](gcs/README.md) — native GCP runtime adapters (GCS JSON API via Workload Identity / ADC, Cloud KMS, bucket lifecycle policy, and `nori-storage-audit`).
- [aws/README.md](aws/README.md) — AWS runtime adapters (S3 artifacts, Secrets Manager, OIDC auth, HS256 service auth, unified `cmd/nori --role=all|api|worker`).
- [Dockerfile](Dockerfile) — digest-pinned distroless static Go image for `cmd/nori`.

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
| `scripts/configure-core.sh` | Changes optional Core settings: the Google connection, Home AI and the LISTEN URL secret. Releases keep existing settings. |
| Repository variable `NORI_AUTO_RELEASE=false` | Stops the scheduled releases. **Run workflow** still releases. |

For the release that introduces issuer-and-subject account binding, Web must ship first. Merge and release the Web change with `scripts/release.sh --web`, then merge the Core change. Keep the gap short, because the Web services bypass their account-resolution caches until Core confirms the binding. After the Core migration, bind existing users that require operator verification using [`nori-user bind-identity`](../README.md#users). Verify sign-in with a provisioned account on each deployed client.

[`scripts/deploy-gcp-mac-web.sh`](../../scripts/deploy-gcp-mac-web.sh) creates the `nori-web-mac` service, its service account and its IAP policy access. After the service exists, the Release workflow updates it with `nori-web`.

The public site runs as `nori-site`. It allows unauthenticated requests, has no IAP and runs as `nori-site@bot-111549.iam.gserviceaccount.com`, which has no roles. The release service account can deploy revisions but can't set `allUsers` access, so `nori-site` was created once by hand with Google's `cloudrun/container/hello` sample image. Until it exists, releases skip the site group. To recreate it:

```bash
gcloud run deploy nori-site --project=bot-111549 --region=us-central1 \
  --image=us-docker.pkg.dev/cloudrun/container/hello \
  --service-account=nori-site@bot-111549.iam.gserviceaccount.com \
  --allow-unauthenticated --no-iap --ingress=all --port=8080 \
  --cpu=1 --memory=256Mi --concurrency=80 --max-instances=10
```

The first release after that replaces the sample revision with the `nori-site:<sha>` image.
