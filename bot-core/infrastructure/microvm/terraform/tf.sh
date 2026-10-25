#!/bin/bash
# Usage: ./tf.sh foundation|runtime init|plan|apply|... [Terraform arguments]
# Local state and Terraform data stay outside the checkout. No remote backup is
# provisioned: losing this directory loses ownership history; retain a private
# backup before moving machines. Terraform's .backup is on the same disk.
set -euo pipefail
umask 077
root=$(cd "$(dirname "$0")" && pwd)
original_args=("$@")
stack=${1:?Usage: tf.sh foundation|runtime command [arguments]}
shift
case "$stack" in foundation|runtime) ;; *) echo 'Unknown stack' >&2; exit 2 ;; esac
command=${1:?Terraform command is required}
shift
# Direct lifecycle/state commands use the same outer lock as computer.sh.
# Ordinary readback stays available during startup or an interactive session.
needs_lock=true
case "$command" in
  output|show|validate|fmt|version|providers|graph|console|-help|--help) needs_lock=false ;;
  state) case "${1:-}" in list|show|pull) needs_lock=false ;; esac ;;
  workspace) case "${1:-}" in list|show) needs_lock=false ;; esac ;;
esac
if "$needs_lock" && ! python3 "$root/../lifecycle-lock.py" --held; then
  exec python3 "$root/../lifecycle-lock.py" "$root/tf.sh" "${original_args[@]}"
fi
state_dir="$HOME/.local/state/nori/bot-111549/terraform"
mkdir -p "$state_dir/$stack-data"
chmod 700 "$state_dir" "$state_dir/$stack-data"
export TF_DATA_DIR="$state_dir/$stack-data"
export TF_IN_AUTOMATION=1
# Only provider operations need credentials. Prefer explicit provider settings or
# ADC; a normal gcloud login is sufficient when neither has been configured.
case "$command" in
  plan|apply|destroy|import|refresh|test)
    # Credential checks must not expand secrets into shell tracing either.
    set +x
    explicit_auth=false
    for setting in GOOGLE_OAUTH_ACCESS_TOKEN GOOGLE_CREDENTIALS GOOGLE_CLOUD_KEYFILE_JSON GCLOUD_KEYFILE_JSON GOOGLE_APPLICATION_CREDENTIALS GOOGLE_IMPERSONATE_SERVICE_ACCOUNT; do
      if [ -n "${!setting:-}" ]; then explicit_auth=true; break; fi
    done
    adc="${CLOUDSDK_CONFIG:-$HOME/.config/gcloud}/application_default_credentials.json"
    if ! "$explicit_auth" && [ ! -e "$adc" ]; then
      # Never expose the captured token through tracing or provider debug logs.
      GOOGLE_OAUTH_ACCESS_TOKEN=$(gcloud --project bot-111549 --quiet auth print-access-token)
      export GOOGLE_OAUTH_ACCESS_TOKEN
      export TF_LOG= TF_LOG_CORE= TF_LOG_PROVIDER=
    fi
    ;;
esac
if [ "$command" = init ]; then
  exec terraform -chdir="$root/$stack" init -backend-config="path=$state_dir/$stack.tfstate" -backend-config="workspace_dir=$state_dir/$stack-workspaces" "$@"
fi
exec terraform -chdir="$root/$stack" "$command" "$@"
