output "worker_pools" {
  value = { for build, pool in google_cloud_run_v2_worker_pool.worker : build => {
    name     = pool.name
    resource = pool.id
  } }
}

output "invoker_service_account" {
  value = try(google_service_account.invoker[0].email, null)
}

output "runner_service_account" {
  value = local.runner
}

output "temporal_api_key_secret" {
  value = "projects/${local.project}/secrets/${local.temporal_api_key_secret}"
}

output "wci_scaler_role" {
  value = try(google_project_iam_custom_role.pool_scaler[0].name, null)
}

output "scalable_build_ids" {
  value = var.scalable_build_ids
}

output "environment" {
  value = var.environment
}

output "computer_instance" {
  value = local.worker_env.COMPUTER_INSTANCE
}

output "temporal_deployment" {
  value = local.deployment
}

output "task_queue" {
  value = local.task_queue
}
