#!/usr/bin/env bash
set -euo pipefail

# Lists resources in the GCP project that infra/ does not manage, such as a
# service created with gcloud. terraform plan only compares the resources it
# already manages, so it can't see these. Exits 1 when it finds any.
# Run from an initialized infra/ with credentials that can read the project.

cd "$(dirname "$0")"
project="bot-111549"
region="us-central1"

state="$(terraform show -json)"
managed() {
  jq -r --arg type "$1" --arg attr "$2" \
    '.values.root_module.resources[]? | select(.type == $type) | .values[$attr] // empty' <<< "$state"
}

found=0
# check LABEL TERRAFORM_TYPE ATTRIBUTE -- COMMAND...
check() {
  local label="$1" type="$2" attr="$3"
  shift 4
  local live extra
  live="$("$@" | sed 's|.*/||' | sort -u)"
  extra="$(comm -23 <(printf '%s\n' "$live" | sed '/^$/d') <(managed "$type" "$attr" | sed 's|.*/||' | sort -u))"
  if [[ -n "$extra" ]]; then
    found=1
    while read -r name; do printf '%s\t%s\n' "$label" "$name"; done <<< "$extra"
  fi
}

g() { gcloud "$@" --project="$project" --quiet; }
# Domain mappings have no GA gcloud list command for a region, so read the API.
domain_mappings() {
  curl --silent --show-error --fail -H "Authorization: Bearer $(gcloud auth print-access-token)" \
    "https://$region-run.googleapis.com/apis/domains.cloudrun.com/v1/namespaces/$project/domainmappings" |
    jq -r '.items[]?.metadata.name'
}

check "Cloud Run service" google_cloud_run_v2_service name -- g run services list --region="$region" --format='value(metadata.name)'
check "Cloud Run job" google_cloud_run_v2_job name -- g run jobs list --region="$region" --format='value(metadata.name)'
check "Domain mapping" google_cloud_run_domain_mapping name -- domain_mappings
check "Service account" google_service_account email -- g iam service-accounts list --filter="email:@$project.iam.gserviceaccount.com" --format='value(email)'
check "Custom role" google_project_iam_custom_role role_id -- g iam roles list --format='value(name)'
check "Secret" google_secret_manager_secret secret_id -- g secrets list --format='value(name)'
check "Bucket" google_storage_bucket name -- g storage buckets list --format='value(name)'
check "Artifact Registry repository" google_artifact_registry_repository repository_id -- g artifacts repositories list --location="$region" --format='value(name)'
check "Cloud SQL instance" google_sql_database_instance name -- g sql instances list --format='value(name)'
check "KMS key ring" google_kms_key_ring name -- g kms keyrings list --location="$region" --format='value(name)'
check "Firestore database" google_firestore_database name -- g firestore databases list --format='value(name)'
check "Workload identity pool" google_iam_workload_identity_pool workload_identity_pool_id -- g iam workload-identity-pools list --location=global --format='value(name)'
check "Global address" google_compute_global_address name -- g compute addresses list --format='value(name)'
check "Forwarding rule" google_compute_global_forwarding_rule name -- g compute forwarding-rules list --format='value(name)'
check "Backend service" google_compute_backend_service name -- g compute backend-services list --format='value(name)'
check "URL map" google_compute_url_map name -- g compute url-maps list --format='value(name)'
check "HTTPS proxy" google_compute_target_https_proxy name -- g compute target-https-proxies list --format='value(name)'
check "SSL certificate" google_compute_managed_ssl_certificate name -- g compute ssl-certificates list --format='value(name)'
check "Network endpoint group" google_compute_region_network_endpoint_group name -- g compute network-endpoint-groups list --format='value(name)'
check "Security policy" google_compute_security_policy name -- g compute security-policies list --format='value(name)'
check "VPC network" google_compute_network name -- g compute networks list --format='value(name)'
check "Firewall rule" google_compute_firewall name -- g compute firewall-rules list --format='value(name)'
check "VM instance" google_compute_instance name -- g compute instances list --format='value(name)'
check "Disk" google_compute_disk name -- g compute disks list --format='value(name)'
check "Snapshot" google_compute_snapshot name -- g compute snapshots list --format='value(name)'
check "Snapshot schedule" google_compute_resource_policy name -- g compute resource-policies list --format='value(name)'
check "Pub/Sub topic" google_pubsub_topic name -- g pubsub topics list --format='value(name)'

if [[ "$found" -eq 0 ]]; then
  echo "Every checked resource in $project is managed by infra/."
fi
exit "$found"
