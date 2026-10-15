ALTER TABLE run_jobs ADD COLUMN fence BIGINT NOT NULL DEFAULT 0;

ALTER TABLE usage_entries ADD COLUMN model_name TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_entries ADD COLUMN profile_name TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX run_jobs_one_active ON run_jobs (run_id) WHERE status IN ('queued', 'claimed');
