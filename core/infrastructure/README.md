# Infrastructure

Nori targets **AWS + Postgres + S3** in production (`cmd/nori --role=all|api|worker`) and **Docker Compose (Postgres only)** locally.

- [local/README.md](local/README.md) — local Postgres Compose stack (`make deps`, `make dev`).
- [aws/README.md](aws/README.md) — AWS runtime adapters (S3 artifacts, Secrets Manager, OIDC auth, HS256 service auth, unified `cmd/nori --role=all|api|worker`).
- [Dockerfile](Dockerfile) — digest-pinned distroless static Go image for `cmd/nori`.

## Cloud Run release

`make deploy-gcp` builds and deploys the core and web images to Cloud Run. In that setup AWS provides runtime adapters such as S3 and Secrets Manager, while Cloud Run hosts the application. Configure a Cloud Run job with the core service's Postgres connectivity and set `NORI_GCP_MIGRATION_JOB` to run schema changes before the core rollout. The script checks core readiness before updating web and checks the web health route afterward. Set `NORI_DEPLOY_SMOKE_TOKEN_FILE` to a file containing a short-lived ID token for a provisioned account to also verify authenticated routing to core.

To roll back a failed release, direct traffic to the last healthy revision of each affected service with `gcloud run services update-traffic SERVICE --to-revisions REVISION=100`. Check core readiness and authenticated web access again. Schema migrations are forward-only. Verify the previous application revision is compatible with the applied schema before shifting traffic.
