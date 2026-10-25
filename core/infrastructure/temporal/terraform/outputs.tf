output "worker_pools" {
  value = { for build, pool in google_cloud_run_v2_worker_pool.worker : build => {
    name         = pool.name
    resource     = pool.id
    shutdown_job = google_cloud_scheduler_job.shutdown[build].id
  } }
}

output "invoker_service_account" {
  value = google_service_account.invoker.email
}

output "runner_service_account" {
  value = local.runner
}

output "temporal_api_key_secret" {
  value = google_secret_manager_secret.temporal_api_key.id
}

output "wci_scaler_role" {
  value = google_project_iam_custom_role.pool_scaler.name
}
