CREATE TABLE IF NOT EXISTS nori_task_leases (
    user_id        text        NOT NULL,
    run_id         text        NOT NULL,
    runnable       boolean     NOT NULL DEFAULT true,
    waiting_reason text        NOT NULL DEFAULT '', -- '', 'question', 'paused', 'routing_pending', 'finishing'
    owner          text        NOT NULL DEFAULT '',
    wake_seq       bigint      NOT NULL DEFAULT 0,
    lease_until    timestamptz,
    wake_at        timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, run_id)
);

CREATE INDEX IF NOT EXISTS nori_task_leases_claimable
    ON nori_task_leases (wake_at, updated_at)
    WHERE runnable = true;

CREATE INDEX IF NOT EXISTS nori_task_leases_waiting
    ON nori_task_leases (user_id, waiting_reason)
    WHERE runnable = false;

CREATE TABLE IF NOT EXISTS nori_user_wakes (
    user_id        text        NOT NULL,
    lane           text        NOT NULL, -- 'conversation' | 'scheduler'
    runnable       boolean     NOT NULL DEFAULT true,
    wake_seq       bigint      NOT NULL DEFAULT 0,
    wake_at        timestamptz NOT NULL DEFAULT now(),
    owner          text        NOT NULL DEFAULT '',
    lease_until    timestamptz,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, lane)
);

CREATE INDEX IF NOT EXISTS nori_user_wakes_due
    ON nori_user_wakes (lane, wake_at, updated_at)
    WHERE runnable = true;
