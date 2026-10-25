# These resources already exist. Import them; do not create duplicates.
import {
  for_each = local.apis
  to       = google_project_service.api[each.value]
  id       = "bot-111549/${each.value}"
}

import {
  to = google_billing_budget.monthly
  id = "billingAccounts/010FFA-6C77F1-18FF6D/budgets/61426b13-0e32-46bd-90f5-3ab891564b9e"
}

import {
  to = google_compute_disk.computer_data
  id = "projects/bot-111549/zones/us-central1-a/disks/bot-computer-data"
}
