#!/bin/bash
# Defaults to printing a plan. --execute is a paid cloud mutation.
set -Eeuo pipefail
project=
execute=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --project) project=${2:?Missing project}; shift 2 ;;
    --execute) execute=true; shift ;;
    *) echo "Usage: $0 --project bot-111549 [--execute]" >&2; exit 2 ;;
  esac
done
[[ "$project" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] || { echo 'Supply a project ID explicitly' >&2; exit 2; }
if "$execute"; then
  echo 'Historical smoke launcher retired: use computer.sh and Terraform runtime lifecycle' >&2
  exit 2
fi
[ "$project" = bot-111549 ] || { echo 'This authorized spike is scoped to bot-111549' >&2; exit 2; }
script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
name="nori-fc-$(date -u +%Y%m%d%H%M%S)-$(python3 -c 'import uuid; print(uuid.uuid4().hex[:8])')"
zone=us-central1-a
region=us-central1
deadline=$(python3 - <<'DEADLINE'
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo
now = datetime.now(timezone.utc)
local = now.astimezone(ZoneInfo("America/Los_Angeles"))
cutoff = local.replace(hour=22, minute=0, second=0, microsecond=0)
if local >= cutoff:
    raise SystemExit("Daily 22:00 America/Los_Angeles cutoff passed; no new paid compute")
print(min(now + timedelta(minutes=30), cutoff).astimezone(timezone.utc).isoformat(timespec="seconds"))
DEADLINE
)
gc=(gcloud --project "$project" --quiet)
print_command() { printf ' '; printf '%q ' "$@"; printf '\n'; }
run() { print_command "$@"; if "$execute"; then "$@"; fi; }
create_vm=("${gc[@]}" compute instances create "$name" --zone "$zone"
  --machine-type n2-standard-2 --image-project ubuntu-os-cloud --image-family ubuntu-2404-lts-amd64
  --boot-disk-size 20GB --boot-disk-type pd-standard --boot-disk-auto-delete
  --enable-nested-virtualization --maintenance-policy TERMINATE --no-restart-on-failure
  --no-service-account --no-scopes --network "$name" --subnet "$name"
  --termination-time "$deadline" --instance-termination-action DELETE
  --labels "purpose=nori-firecracker-smoke"
  --metadata block-project-ssh-keys=TRUE,serial-port-enable=FALSE
  --metadata-from-file "startup-script=$script_dir/host-startup.sh")
echo "Disposable one-host experiment: project=$project name=$name deadline=$deadline"
echo 'One ephemeral IPv4; no ingress rules, SSH keys, service account, guest network, or extra services.'
if ! "$execute"; then
  run "${gc[@]}" compute networks create "$name" --subnet-mode custom
  run "${gc[@]}" compute networks subnets create "$name" --region "$region" --network "$name" --range 10.231.0.0/24
  run "${create_vm[@]}"
  echo 'Execution polls serial output for up to 15 minutes, then removes exact VM/disk/subnet/VPC and verifies absence.'
  echo 'Absolute host deletion deadline is a fallback, not an exact billing cap; VPC cleanup still needs the local runner.'
  exit 0
fi

# List errors are failures, never evidence of absence. Names are unique and
# preflighted; cleanup is armed before creation to cover uncertain create errors.
listed() {
  case "$1" in
    vm) "${gc[@]}" compute instances list --zones "$zone" --filter "name=$name" --format 'value(name)' ;;
    disk) "${gc[@]}" compute disks list --zones "$zone" --filter "name=$name" --format 'value(name)' ;;
    subnet) "${gc[@]}" compute networks subnets list --regions "$region" --filter "name=$name" --format 'value(name)' ;;
    network) "${gc[@]}" compute networks list --filter "name=$name" --format 'value(name)' ;;
  esac
}
for kind in vm disk subnet network; do
  existing=$(listed "$kind")
  [ -z "$existing" ] || { echo "Refusing existing $kind: $name" >&2; exit 1; }
done
remove_exact() {
  local kind=$1 present
  present=$(listed "$kind") || return 1
  [ -n "$present" ] || return 0
  case "$kind" in
    vm) "${gc[@]}" compute instances delete "$name" --zone "$zone" --delete-disks boot ;;
    disk) "${gc[@]}" compute disks delete "$name" --zone "$zone" ;;
    subnet) "${gc[@]}" compute networks subnets delete "$name" --region "$region" ;;
    network) "${gc[@]}" compute networks delete "$name" ;;
  esac
}
cleanup() {
  local original=$? failed=0 kind present
  trap - EXIT INT TERM
  set +e
  for kind in vm disk subnet network; do
    if ! remove_exact "$kind"; then
      echo "CLEANUP FAILED: $kind $name; project=$project zone=$zone region=$region" >&2
      failed=1
    fi
  done
  for kind in vm disk subnet network; do
    present=$(listed "$kind")
    if [ "$?" -ne 0 ] || [ -n "$present" ]; then
      echo "ABSENCE NOT VERIFIED: $kind $name" >&2
      failed=1
    else
      echo "ABSENT: $kind $name"
    fi
  done
  if [ "$original" -eq 0 ] && [ "$failed" -ne 0 ]; then original=1; fi
  exit "$original"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
run "${gc[@]}" compute networks create "$name" --subnet-mode custom
run "${gc[@]}" compute networks subnets create "$name" --region "$region" --network "$name" --range 10.231.0.0/24
run "${create_vm[@]}"
evidence=$(mktemp -d "${TMPDIR:-/tmp}/nori-firecracker-smoke.XXXXXX")
echo "Serial evidence: $evidence/serial.log"
end=$((SECONDS + 900))
while [ "$SECONDS" -lt "$end" ]; do
  "${gc[@]}" compute instances get-serial-port-output "$name" --zone "$zone" --port 1 --start 0 | tr -d '\r' > "$evidence/serial.log"
  if grep -q '^NORI_FC_FAIL' "$evidence/serial.log"; then grep -E 'NORI_FC_|panic|mount:|^Linux ' "$evidence/serial.log" | tail -40; exit 1; fi
  if grep -q '^NORI_FC_SUCCESS' "$evidence/serial.log"; then
    grep -E '^NORI_FC_|^Linux ' "$evidence/serial.log"
    echo 'Verified two guest boots and rootfs marker survival; no worker/browser/network execution was exercised.'
    exit 0
  fi
  sleep 10
done
tail -40 "$evidence/serial.log"
echo 'Timed out waiting for guest proof' >&2
exit 1
