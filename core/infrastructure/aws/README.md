# AWS adapters

`NORI_CLOUD=aws` (the default for hosted `cmd/nori`) selects S3 files, AWS Secrets
Manager, OIDC sign-in, and signed service calls over Postgres.
`NORI_CLOUD=local` (the default for `cmd/nori-local`) selects local files over local
Postgres, without a secret service.

Nori's production runtime is a single Go binary (`cmd/nori --role=all|api|worker`)
backed by **Postgres + S3** — no external workflow engine or queue service is required.

This is application support, not a provisioned AWS environment. There is no AWS
Terraform or deployment in this repository.

## Configuration

Use the main checkout's gitignored, mode-600 `.env`, or the existing root-owned
server env file outside the repository. Keep the current model keys and models.
AWS SDK clients use workload IAM roles or the standard AWS credential chain.

| Setting | Purpose |
| --- | --- |
| `NORI_CLOUD=aws`, `AWS_REGION` | Select AWS adapters and region |
| `NORI_ROLE=all\|api\|worker` | Runtime role for `cmd/nori` (`--role` flag overrides; defaults to `all`) |
| `NORI_POSTGRES_URL` | Private Postgres connection, with production TLS |
| `ARTIFACT_BUCKET` | Private S3 bucket |
| `NORI_SERVICE_TOKEN_KEY`, `NORI_SERVICE_AUDIENCE` | Shared service signing secret of at least 32 random bytes; audience defaults to `nori` |
| `NORI_CREDENTIAL_KEY` or `NORI_CREDENTIAL_KEY_SECRET` | 32-byte master key (64 hex chars or base64) or AWS Secrets Manager secret name/ARN for envelope-encrypted Postgres credential rows |

S3 keeps the existing `artifacts/` layout and user prefixes. Conditional writes
refuse overwrites. Reads remain bounded by the existing artifact and upload limits.
Files stay private; the API serves them after resolving the authenticated user.
See [S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html).

## Single binary & Postgres-native execution

Run `cmd/nori` in one of three roles:

- `--role=all` (default): runs HTTP + SSE API alongside the unified Postgres-native
  worker pool (outer conversation lane, outer scheduler lane, and inner task pool)
  in one process.
- `--role=api`: runs HTTP + SSE API only.
- `--role=worker`: runs the Postgres-native worker pool only for independent
  horizontal scaling.

All authoritative state (`nori_records`), task discovery (`nori_task_leases`), and
outer conversation/scheduler wakes (`nori_user_wakes`) live in Postgres:

- Mutations write durable wake rows inside the same transaction and emit post-commit
  `NOTIFY nori_wake` hints.
- Each worker pod holds one dedicated `LISTEN nori_wake` connection plus a 2-second
  fallback poll across all lanes and a 60-second repair sweep reconciling discovery
  rows against `nori_records`.
- Delayed automations store their exact `wake_at` timestamp in `nori_user_wakes`
  with no delay cap.
- On `SIGTERM`, `cmd/nori` stops claiming new work immediately, signals in-flight
  task runners to stop at the next checkpoint boundary (up to a 25-second drain
  window), and releases leases cleanly so another replica can resume without waiting
  for lease expiry.

## Sign-in and service authentication

Hosted AWS deployments use OIDC (`NORI_OIDC_ISSUER` and `NORI_OIDC_AUDIENCE`).
The web server discovers the issuer's HTTPS JWKS and verifies the signature, exact
issuer, audience, expiry, subject and verified email. The issuer and subject identify
the provisioned Nori user. Key fetching
and verification use [jose](https://github.com/panva/jose).

A login gateway or host supplies an OIDC **ID token**, either as a Bearer token to
the web server or in the cookie named by `NORI_OIDC_COOKIE`. The gateway must make
that cookie Secure, HttpOnly and SameSite, and handle sign-in, refresh, logout and
CSRF for its own login flow. Cookie delivery supports browser navigation and SSE.
This adapter verifies tokens; it does not implement a new OAuth login UI or
callback. Providers must include `email` and boolean `email_verified=true`.

The trusted web server resolves the verified identity at the API and supplies the resulting
user ID. Hosted API entrypoints require HS256 service tokens with the configured
audience, `nori-service` issuer and a one-minute lifetime. Only `/healthz` is
unauthenticated. The signing secret belongs only to trusted services, not browsers.
Loopback development uses the existing local identity flow.
