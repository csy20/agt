CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL DEFAULT '',
    request_hash TEXT NOT NULL,
    goal TEXT NOT NULL,
    repo_path TEXT NOT NULL,
    worktree TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    state_version BIGINT NOT NULL,
    risk_level TEXT NOT NULL DEFAULT 'read',
    budget_cents INTEGER NOT NULL,
    spent_micros BIGINT NOT NULL DEFAULT 0,
    agent_version TEXT NOT NULL DEFAULT '',
    trace_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    script_path TEXT NOT NULL DEFAULT '',
    script_json JSONB,
    plan_json JSONB,
    result_json JSONB,
    error TEXT NOT NULL DEFAULT '',
    step_count INTEGER NOT NULL DEFAULT 0,
    max_steps INTEGER NOT NULL,
    deadline_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX runs_idempotency_key_uq
    ON runs (idempotency_key)
    WHERE idempotency_key <> '';

CREATE TABLE run_events (
    run_id TEXT NOT NULL REFERENCES runs (id),
    sequence_no BIGINT NOT NULL,
    event_type TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, sequence_no)
);

CREATE TABLE commands (
    command_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs (id),
    tool TEXT NOT NULL,
    canonical_args JSONB NOT NULL,
    args_hash TEXT NOT NULL,
    policy_decision TEXT NOT NULL,
    approval_id TEXT,
    state TEXT NOT NULL,
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX commands_run_id_idx ON commands (run_id, created_at);

CREATE TABLE approvals (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs (id),
    command_id TEXT NOT NULL REFERENCES commands (command_id),
    args_hash TEXT NOT NULL,
    approver TEXT NOT NULL,
    decision TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE usage_entries (
    id BIGSERIAL PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs (id),
    kind TEXT NOT NULL,
    quantity BIGINT NOT NULL,
    unit_price_micros BIGINT NOT NULL DEFAULT 0,
    cost_micros BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX usage_entries_run_id_idx ON usage_entries (run_id);

CREATE TABLE run_jobs (
    id BIGSERIAL PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs (id),
    status TEXT NOT NULL,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX run_jobs_claim_idx ON run_jobs (status, id);
