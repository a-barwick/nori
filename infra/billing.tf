# GCP Billing Budget guardrail for Nori's project.
#
# Gated on billing_account in nori-terraform-private so environments without a
# configured billing account (and terraform validate) stay clean. Alerts email
# Billing Account Admins and Users by default at 50%, 80%, 100% actual spend and
# 100% forecasted spend.

resource "google_billing_budget" "nori_monthly" {
  count = try(local.private.billing_account, "") != "" ? 1 : 0

  billing_account = local.private.billing_account
  display_name    = "Nori Monthly Spend Guardrail"

  budget_filter {
    projects = ["projects/${local.project_number}"]
  }

  amount {
    specified_amount {
      currency_code = "USD"
      units         = tostring(try(local.private.billing_monthly_budget_usd, 150))
    }
  }

  threshold_rules {
    threshold_percent = 0.5
    spend_basis       = "CURRENT_SPEND"
  }

  threshold_rules {
    threshold_percent = 0.8
    spend_basis       = "CURRENT_SPEND"
  }

  threshold_rules {
    threshold_percent = 1.0
    spend_basis       = "CURRENT_SPEND"
  }

  threshold_rules {
    threshold_percent = 1.0
    spend_basis       = "FORECASTED_SPEND"
  }
}
