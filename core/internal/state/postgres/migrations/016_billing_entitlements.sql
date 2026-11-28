-- Track A monetization, unified micro-USD credit ledger, Stripe/Apple IAP ports,
-- fleet-wide daily hosted budgets, GCS storage quota tracking, and Sprites metering columns.
--
-- Note on Row-Level Security: Like nori_user_computers and nori_push_devices, the new
-- billing/entitlement/budget/storage tables below are system-owned tables accessed by
-- backend services with explicit user_id predicates (no tenant RLS on these tables).

CREATE TABLE IF NOT EXISTS nori_user_entitlements (
    user_id TEXT PRIMARY KEY,
    tier TEXT NOT NULL CHECK (tier IN ('trial', 'byok', 'standard', 'power')),
    pending_tier TEXT NULL CHECK (pending_tier IN ('byok', 'standard', 'power')),
    status TEXT NOT NULL CHECK (status IN ('active', 'billing_retry', 'past_due', 'canceled', 'expired')),
    billing_provider TEXT NOT NULL DEFAULT 'manual' CHECK (billing_provider IN ('manual', 'stripe', 'apple_iap')),
    period_start TIMESTAMPTZ NOT NULL DEFAULT now(),
    period_end TIMESTAMPTZ NULL,
    grace_until TIMESTAMPTZ NULL,
    credit_override_micros BIGINT NULL,
    app_account_token UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    suspended_at TIMESTAMPTZ NULL,
    suspended_reason TEXT NOT NULL DEFAULT '',
    provider_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS nori_subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id TEXT NOT NULL,
    provider TEXT NOT NULL CHECK (provider IN ('manual', 'stripe', 'apple_iap')),
    environment TEXT NOT NULL DEFAULT 'production' CHECK (environment IN ('production', 'sandbox')),
    external_customer_id TEXT NOT NULL DEFAULT '',
    external_subscription_id TEXT NOT NULL,
    external_product_id TEXT NOT NULL DEFAULT '',
    tier TEXT NOT NULL CHECK (tier IN ('byok', 'standard', 'power')),
    status TEXT NOT NULL CHECK (status IN ('active', 'billing_retry', 'past_due', 'canceled', 'expired')),
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NULL,
    grace_until TIMESTAMPTZ NULL,
    provider_updated_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, environment, external_subscription_id)
);

CREATE INDEX IF NOT EXISTS nori_subscriptions_user_status
    ON nori_subscriptions (user_id, status);

CREATE TABLE IF NOT EXISTS nori_entitlement_events (
    provider TEXT NOT NULL,
    event_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    environment TEXT NOT NULL DEFAULT 'production' CHECK (environment IN ('production', 'sandbox')),
    provider_updated_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, event_id)
);

CREATE INDEX IF NOT EXISTS nori_entitlement_events_user
    ON nori_entitlement_events (user_id, applied_at DESC);

CREATE TABLE IF NOT EXISTS nori_credit_grants (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('trial', 'period', 'topup', 'promo', 'clawback')),
    amount_micros BIGINT NOT NULL,
    valid_from TIMESTAMPTZ NOT NULL,
    valid_until TIMESTAMPTZ NULL,
    source_provider TEXT NOT NULL DEFAULT 'manual',
    source_event_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS nori_credit_grants_user_validity
    ON nori_credit_grants (user_id, valid_from, valid_until);

CREATE TABLE IF NOT EXISTS nori_global_budget_daily (
    day DATE NOT NULL,
    scope TEXT NOT NULL CHECK (scope IN ('hosted_all', 'hosted_llm', 'hosted_exa', 'hosted_sprites', 'trials_created')),
    spent_micros BIGINT NOT NULL DEFAULT 0,
    count INT NOT NULL DEFAULT 0,
    PRIMARY KEY (day, scope)
);

CREATE TABLE IF NOT EXISTS nori_user_storage (
    user_id TEXT PRIMARY KEY,
    active_bytes BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backfill pre-existing users as manual active BYOK so no existing account spends platform credits unexpectedly.
INSERT INTO nori_user_entitlements (user_id, tier, status, billing_provider, period_start, provider_updated_at, updated_at)
SELECT id, 'byok', 'active', 'manual', now(), now(), now()
FROM nori_records
WHERE shard = 'users' AND collection = 'users'
ON CONFLICT (user_id) DO NOTHING;

-- Backfill pre-016 historical usage records so they are tagged 'byok' (or 'local') and never drain post-016 hosted credits.
UPDATE nori_records
SET data = jsonb_set(
    data,
    '{billing}',
    CASE
        WHEN COALESCE(data->>'cost_status', '') = 'local' THEN '"local"'::jsonb
        ELSE '"byok"'::jsonb
    END,
    true
)
WHERE collection LIKE 'owners/%/model_usage'
  AND COALESCE(data->>'billing', '') = '';

-- Alter nori_user_computers LAST so ACCESS EXCLUSIVE lock is not held during the nori_records backfill above.
ALTER TABLE nori_user_computers
    ADD COLUMN IF NOT EXISTS last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE nori_user_computers
    ADD COLUMN IF NOT EXISTS awake_since TIMESTAMPTZ NULL;
ALTER TABLE nori_user_computers
    ADD COLUMN IF NOT EXISTS last_metered_at TIMESTAMPTZ NULL;
ALTER TABLE nori_user_computers
    ADD COLUMN IF NOT EXISTS sleeping_since TIMESTAMPTZ NULL;
ALTER TABLE nori_user_computers
    ADD COLUMN IF NOT EXISTS provider_owner TEXT NOT NULL DEFAULT 'platform';
