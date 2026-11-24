# Infrastructure

Nori targets **AWS + Postgres + S3** in production (`cmd/nori --role=all|api|worker`) and **Docker Compose (Postgres only)** locally.

- [local/README.md](local/README.md) — local Postgres Compose stack (`make deps`, `make dev`).
- [aws/README.md](aws/README.md) — AWS runtime adapters (S3 artifacts, Secrets Manager, OIDC auth, HS256 service auth, unified `cmd/nori --role=all|api|worker`).
- [Dockerfile](Dockerfile) — digest-pinned distroless static Go image for `cmd/nori`.
