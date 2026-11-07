# AWS adapters

`NORI_CLOUD=aws` selects S3 files, SQS wakes with EventBridge Scheduler for future
delivery, Secrets Manager, an EC2 computer, OIDC sign-in and signed service calls.
Postgres becomes the default database; an explicit `NORI_STORE_BACKEND` still wins.
GCP remains the default cloud and retains its existing adapters. `local` selects
local files, in-process wakes and a loopback computer, without a secret service.

This is application support, not a provisioned AWS environment. There is no AWS
Terraform or deployment in this change. Local SDK/HTTP fixtures verify request and
response behavior; AWS IAM, a retained EC2 guest, an SSM tunnel and a real identity
provider still need deployment-specific validation.

## Configuration

Use the main checkout's gitignored, mode-600 `.env`, or the existing root-owned
server env file outside the repository. Keep the current model keys and models.
AWS SDK clients use workload IAM roles or the standard AWS credential chain.

| Setting | Purpose |
| --- | --- |
| `NORI_CLOUD=aws`, `AWS_REGION` | Select adapters and region |
| `NORI_POSTGRES_URL` | Private Postgres connection, with production TLS |
| `ARTIFACT_BUCKET` | Private S3 bucket |
| `NORI_RUNTIME_URL` | HTTPS root of the Go API, without query or credentials |
| `NORI_AWS_WAKE_QUEUE_URL`, `NORI_AWS_WAKE_QUEUE_ARN` | Existing standard SQS queue |
| `NORI_AWS_SCHEDULE_ROLE_ARN`, `NORI_AWS_SCHEDULE_GROUP` | Scheduler execution role and group (default group when omitted) |
| `NORI_AWS_WAKE_DLQ_ARN` | SQS dead-letter queue for failed scheduled delivery |
| `NORI_WORKER_SERVICES` | Optional JSON map from immutable worker build to HTTPS service root; omitted means workers poll Temporal |
| `NORI_SERVICE_TOKEN_KEY`, `NORI_SERVICE_AUDIENCE` | Shared service signing secret of at least 32 random bytes; audience defaults to `nori` |
| `TEMPORAL_API_KEY_SECRET_VERSION` | Optional Secrets Manager name or ARN; the existing variable is retained for compatibility |
| User row `computer.instance` | Retained EC2 instance ID; `AWS_REGION` chooses its region |

S3 keeps the existing `artifacts/` layout and user prefixes. Conditional writes
refuse overwrites. Reads remain bounded by the existing artifact and upload limits.
Files stay private; the API serves them after resolving the authenticated user.
See [S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html).

## Wakes and retry delivery

Run `go run ./cmd/nori-wake-consumer` from `core/` as a separate, supervised process.
It needs the queue settings, runtime and worker routes, and service signing secret.
It keeps two deliveries active so a long worker request can coexist with a
conversation wake. The API and worker do not consume SQS themselves.

Immediate wakes go straight to SQS. Future wakes use a named, one-time Scheduler
target that sends to the same queue. This avoids SQS's short delay limit without
holding a process or periodically re-enqueuing distant schedules. Scheduler has
minute-level precision. Names include the user and stable wake key; completed
schedules are deleted. Scheduler retries target delivery for up to 24 hours and
185 attempts, then retains failures in its configured DLQ.

The consumer only deletes after an HTTP 2xx response. Handler failures, a crash or
an ambiguous delete cause redelivery; handlers retain their existing duplicate
protection. Each HTTP delivery has a 12-minute deadline within a 13-minute SQS
visibility window. A failed request becomes visible again after 30 seconds.
Bodies contain user/workflow identities and trace context, never conversation
content, target URLs or credentials. Targets come from settings and the consumer
mints a fresh, one-minute service token for each attempt.

Configure queue retention and an SQS redrive policy/DLQ, and monitor both that DLQ
and the Scheduler DLQ. Replay retained failures after repairing the cause; finite
AWS retention is not an unlimited delivery guarantee. Keep routes for retained
worker builds. No queue, role, DLQ or policy is created automatically.

Relevant permissions are SQS send/receive/delete/change-visibility, Scheduler
create-schedule and IAM pass-role. The Scheduler role needs permission to send to
the wake queue and its DLQ. Scope all of them to the configured resources.
[Scheduler targets](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_Target.html)
and [SQS receive/visibility](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)
describe the delivery boundary.

## Sign-in and service authentication

AWS defaults to OIDC; `NORI_AUTH_PROVIDER=iap` keeps IAP available explicitly.
Configure `NORI_OIDC_ISSUER` and `NORI_OIDC_AUDIENCE`. The web server discovers the
issuer's HTTPS JWKS and verifies the signature, exact issuer, audience, expiry,
subject and verified email. The email must match a provisioned Nori user. Unsigned
identity headers are never accepted. Key fetching and verification use
[jose](https://github.com/panva/jose).

A login gateway or host supplies an OIDC **ID token**, either as a Bearer token to
the web server or in the cookie named by `NORI_OIDC_COOKIE`. The gateway must make
that cookie Secure, HttpOnly and SameSite, and handle sign-in, refresh, logout and
CSRF for its own login flow. Cookie delivery supports browser navigation and SSE.
This adapter verifies tokens; it does not implement a new OAuth login UI or
callback. Providers must include `email` and boolean `email_verified=true`.

The trusted web server resolves the email at the API and supplies the resulting
user ID. AWS API and worker entrypoints require HS256 service tokens with the
configured audience, `nori-service` issuer and a one-minute lifetime. Only
`/healthz` is unauthenticated. The signing secret belongs only to trusted services,
not browsers or queue messages. GCP continues using managed service-account IAM.
Loopback development uses the existing local identity flow.

## Retained computer

The adapter observes, starts and stops the existing instance; it never creates or
terminates one. `Attach` never starts compute. Stopping and terminated instances
are distinct states, so cleanup cannot mistake a still-stopping machine for a
stopped one. Routine shutdown remains the guest's existing idle shutdown; configure
EC2 instance-initiated shutdown behavior to stop and retain the attached disks.

Guest RPC uses AWS CLI `ssm start-session` with `AWS-StartPortForwardingSession` to
port 8090, matching the existing tunnel lifecycle. The caller needs AWS CLI plus
Session Manager plugin, and the instance needs SSM Agent, its instance role and
connectivity to SSM. No public RPC port is needed. IAM must permit describing,
starting/stopping that instance and opening the SSM session. A separate operator
can use the adapter's `OpenHost`/`Stop` operation; application cleanup does not
stop a shared computer. See [SSM port forwarding](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-sessions-start.html).
