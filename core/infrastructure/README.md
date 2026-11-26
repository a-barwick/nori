# Infrastructure

Nori targets **GCP + Postgres + GCS** (`NORI_CLOUD=gcp`) or **AWS + Postgres + S3** (`NORI_CLOUD=aws`) in production (`cmd/nori --role=all|api|worker`) and **Docker Compose (Postgres only)** locally (`NORI_CLOUD=local`).

- [local/README.md](local/README.md) — local Postgres Compose stack (`make deps`, `make dev`).
- [gcs/README.md](gcs/README.md) — native GCP runtime adapters (GCS JSON API via Workload Identity / ADC, Cloud KMS, bucket lifecycle policy, and `nori-storage-audit`).
- [aws/README.md](aws/README.md) — AWS runtime adapters (S3 artifacts, Secrets Manager, OIDC auth, HS256 service auth, unified `cmd/nori --role=all|api|worker`).
- [Dockerfile](Dockerfile) — digest-pinned distroless static Go image for `cmd/nori`.

## Cloud Run release

CI releases on every green push to `main`. After the checks pass, the **Build images** job pushes `nori-core:<sha>` and `nori-web:<sha>` to Artifact Registry. The **Release** job then runs [`scripts/release.sh`](../../scripts/release.sh), using keyless GitHub-to-GCP auth (Workload Identity Federation, pool `github`, limited to `a-barwick/nori` on `main`).

- **Only what changed ships.** Each revision carries a `nori-commit` label. The script compares it with the new commit and releases the **core** group (the `nori-migrate` job, then `nori-core` and `nori-worker`) when `core/` or `contracts/` changed, and the **web** group (`nori-web` and `nori-web-mac`, same image) when `web/` or `contracts/` changed.
- **Staged, then promoted.** Each service is deployed with no traffic and must pass its HTTP startup probe (`/readyz` or `/api/health`). `nori-core` must also answer `/readyz` on its tagged URL, and `nori-web-mac` must reject anonymous requests. Then the service moves to 100%.
- **Bake and auto-revert.** For 10 minutes after promotion, any 5xx or container crash from a new revision sends that group back to the revision recorded in its `nori-prev` label.
- **Migrations only add schema.** `TestMigrationsOnlyAddSchema` enforces it, so a revert always runs against a compatible schema. Ship removals and renames later, in a `*_cleanup.sql` migration.

Manual operations:

- `scripts/release.sh --dry-run` shows what would ship. It refuses commits that aren't on `origin/main` or whose CI checks haven't passed; `--force-unverified` overrides that in an emergency. `--core` and `--web` force a group.
- `scripts/release.sh rollback core|web|all` returns each service in the group to its previous revision.
- `scripts/configure-core.sh` changes optional Core settings (the Google Workspace beta, Home AI, the LISTEN URL secret). Releases keep existing settings.
- Set the repository variable `NORI_AUTO_RELEASE=false` to pause automatic releases.

For the release that introduces issuer-and-subject account binding, Web must go first. Release it with `scripts/release.sh --web` before the Core change lands, then let CI release Core. Keep the gap short, because the Web services bypass their account-resolution caches until Core confirms the binding. After the Core migration, bind existing users that require operator verification using [`nori-user bind-identity`](../README.md#users). Verify sign-in with a provisioned account on each deployed client.

[`scripts/deploy-gcp-mac-web.sh`](../../scripts/deploy-gcp-mac-web.sh) creates the `nori-web-mac` service, its identity, and its IAP policy access the first time. After that, CI releases it with Web.
