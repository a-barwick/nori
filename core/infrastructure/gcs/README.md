# Google Cloud Storage & Native GCP Runtime

`NORI_CLOUD=gcp` runs Nori natively on GCP (Cloud Run + Postgres + Google Cloud Storage + Cloud KMS) using Application Default Credentials (Workload Identity) with no AWS SDK credentials or HMAC keys required.

## Configuration

| Setting | Purpose |
| --- | --- |
| `NORI_CLOUD=gcp` | Select native GCS artifact storage and GCP runtime validation |
| `ARTIFACT_BUCKET` | Required private GCS bucket name |
| `NORI_POSTGRES_URL` | Private Postgres connection with production TLS |
| `NORI_SERVICE_TOKEN_KEY`, `NORI_SERVICE_AUDIENCE` | Shared HS256 service signing secret (at least 32 bytes; audience defaults to `nori`) |
| `NORI_CREDENTIAL_KMS_KEY` | Cloud KMS crypto key resource name for envelope-encrypted Postgres credentials and artifacts |

`NORI_CREDENTIAL_KEY_SECRET` is AWS Secrets Manager-only and is rejected at startup when `NORI_CLOUD=gcp`.

The Cloud Run service account needs `roles/storage.objectAdmin` scoped to `ARTIFACT_BUCKET` (and `roles/cloudkms.cryptoKeyEncrypterDecrypter` on `NORI_CREDENTIAL_KMS_KEY` when using Cloud KMS).

Native GCS uses the exact same object key layout (`artifacts/users/<id>/...`), conditional create (`ifGenerationMatch=0`), and `Cache-Control: private, no-store` as the S3 adapter, so switching an existing bucket from `NORI_CLOUD=aws` to `NORI_CLOUD=gcp` requires zero data migration.

## Bucket Lifecycle Policy

`lifecycle.json` enforces two automatic cleanup rules on `ARTIFACT_BUCKET`:

1. **Abort incomplete multipart uploads after 1 day** (`AbortIncompleteMultipartUpload`, `age: 1`).
2. **Delete noncurrent object versions after 7 days** (`Delete`, `isLive: false`, `daysSinceNoncurrentTime: 7`) when object versioning is enabled.

Apply the lifecycle policy:

```bash
gcloud storage buckets update "gs://${ARTIFACT_BUCKET}" \
  --lifecycle-file=core/infrastructure/gcs/lifecycle.json
```

Verify the active policy:

```bash
gcloud storage buckets describe "gs://${ARTIFACT_BUCKET}" \
  --format="default(lifecycle)"
```

## Orphan Blob Audit & Reconciliation

`core/cmd/nori-storage-audit` compares live GCS objects under `artifacts/users/<id>/` against Postgres `uploads`, `results`, and `runs` references for each user (skipping users with pending resets or active data locks).

Dry-run (default — deletes nothing):

```bash
cd core && go run ./cmd/nori-storage-audit --bucket "${ARTIFACT_BUCKET}" --grace 24h
```

Delete verified orphans older than 24h (explicit `--confirm` required, capped by `--max-delete`):

```bash
cd core && go run ./cmd/nori-storage-audit \
  --bucket "${ARTIFACT_BUCKET}" \
  --grace 24h \
  --delete \
  --confirm \
  --max-delete 500
```
