#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Apply optional Nori Core settings to nori-core and nori-worker. Releases keep these settings;
use this only when a setting changes. Each update starts a new revision with the live image.

Usage: scripts/configure-core.sh [--dry-run]

Environment variables (set only the groups you are changing):
  NORI_POSTGRES_LISTEN_URL_SECRET           Secret Manager ref for the LISTEN/NOTIFY Postgres URL
  NORI_GOOGLE_WORKSPACE_CLIENT_SECRET_REF   Secret ref for the Google connection, with
    NORI_GOOGLE_WORKSPACE_CLIENT_ID and NORI_GOOGLE_WORKSPACE_REDIRECT_URL. Optional
    NORI_GOOGLE_WORKSPACE_TEST_USERS limits Google to those emails. With the Google
    settings above, set it to an empty string to remove the limit and offer Google to
    every user, or leave it unset to keep the current value.
  NORI_HOME_AI_KEY_SECRET                   Shared Home AI key secret (name:version), with
    NORI_HOME_AI_VERIFY_URL and optional NORI_HOME_AI_WORK_ENABLED, NORI_HOME_AI_CORE_DB_SECRET,
    NORI_HOME_AI_CORE_DB_LISTEN_SECRET, NORI_HOME_AI_DOWNLOAD_MACOS,
    NORI_HOME_AI_DOWNLOAD_LINUX_AMD64, NORI_HOME_AI_DOWNLOAD_LINUX_ARM64
USAGE
}

dry_run=0
case "${1:-}" in
  "") ;;
  --dry-run) dry_run=1 ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac

project="${GCP_PROJECT:-bot-111549}"
region="${GCP_REGION:-us-central1}"
secrets=()
env_vars=()
remove_env_vars=()

if [[ -n "${NORI_POSTGRES_LISTEN_URL_SECRET:-}" ]]; then
  secrets+=("NORI_POSTGRES_LISTEN_URL=$NORI_POSTGRES_LISTEN_URL_SECRET")
fi
if [[ -n "${NORI_GOOGLE_WORKSPACE_CLIENT_SECRET_REF:-}" ]]; then
  : "${NORI_GOOGLE_WORKSPACE_CLIENT_ID:?Set the Google OAuth client ID}"
  : "${NORI_GOOGLE_WORKSPACE_REDIRECT_URL:?Set the Google OAuth redirect URL}"
  secrets+=("NORI_GOOGLE_WORKSPACE_CLIENT_SECRET=$NORI_GOOGLE_WORKSPACE_CLIENT_SECRET_REF")
  env_vars+=(
    "NORI_GOOGLE_WORKSPACE_CLIENT_ID=$NORI_GOOGLE_WORKSPACE_CLIENT_ID"
    "NORI_GOOGLE_WORKSPACE_REDIRECT_URL=$NORI_GOOGLE_WORKSPACE_REDIRECT_URL"
  )
  # Unset keeps the live tester list, an empty value removes it, and a value replaces it.
  if [[ -n "${NORI_GOOGLE_WORKSPACE_TEST_USERS+set}" ]]; then
    if [[ -n "$NORI_GOOGLE_WORKSPACE_TEST_USERS" ]]; then
      env_vars+=("NORI_GOOGLE_WORKSPACE_TEST_USERS=$NORI_GOOGLE_WORKSPACE_TEST_USERS")
    else
      remove_env_vars+=("NORI_GOOGLE_WORKSPACE_TEST_USERS")
    fi
  fi
fi
if [[ -n "${NORI_HOME_AI_KEY_SECRET:-}" ]]; then
  : "${NORI_HOME_AI_VERIFY_URL:?Set the Nori Web pairing URL}"
  secrets+=("NORI_HOME_AI_KEY=$NORI_HOME_AI_KEY_SECRET")
  [[ -z "${NORI_HOME_AI_CORE_DB_SECRET:-}" ]] || secrets+=("NORI_HOME_AI_POSTGRES_URL=$NORI_HOME_AI_CORE_DB_SECRET")
  [[ -z "${NORI_HOME_AI_CORE_DB_LISTEN_SECRET:-}" ]] || secrets+=("NORI_HOME_AI_POSTGRES_LISTEN_URL=$NORI_HOME_AI_CORE_DB_LISTEN_SECRET")
  env_vars+=("NORI_HOME_AI_VERIFY_URL=$NORI_HOME_AI_VERIFY_URL" "NORI_HOME_AI_WORK_ENABLED=${NORI_HOME_AI_WORK_ENABLED:-false}")
  for name in NORI_HOME_AI_DOWNLOAD_MACOS NORI_HOME_AI_DOWNLOAD_LINUX_AMD64 NORI_HOME_AI_DOWNLOAD_LINUX_ARM64; do
    [[ -z "${!name:-}" ]] || env_vars+=("$name=${!name}")
  done
fi

if [[ "${#secrets[@]}" -eq 0 && "${#env_vars[@]}" -eq 0 && "${#remove_env_vars[@]}" -eq 0 ]]; then
  echo "Nothing to configure. Set the variables for the setting you are changing." >&2
  exit 2
fi

args=()
if [[ "${#secrets[@]}" -gt 0 ]]; then args+=(--update-secrets="$(IFS=,; printf '%s' "${secrets[*]}")"); fi
# The alternate delimiter preserves comma-separated values such as the tester allowlist.
if [[ "${#env_vars[@]}" -gt 0 ]]; then args+=(--update-env-vars="^|^$(IFS='|'; printf '%s' "${env_vars[*]}")"); fi
if [[ "${#remove_env_vars[@]}" -gt 0 ]]; then args+=(--remove-env-vars="$(IFS=,; printf '%s' "${remove_env_vars[*]}")"); fi

for service in nori-core nori-worker; do
  echo "+ gcloud run services update $service ${args[*]}"
  if [[ "$dry_run" -eq 0 ]]; then
    gcloud run services update "$service" --project="$project" --region="$region" --quiet "${args[@]}"
  fi
done
