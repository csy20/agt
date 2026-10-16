-- A feature or refactor run stores the contract the user supplied.
-- Later model output cannot replace it. Repair and older rows keep the defaults.

ALTER TABLE runs ADD COLUMN task_type TEXT NOT NULL DEFAULT 'repair';
ALTER TABLE runs ADD CONSTRAINT runs_task_type_chk CHECK (task_type IN ('repair', 'feature', 'refactor'));
ALTER TABLE runs ADD COLUMN contract_json JSONB NOT NULL DEFAULT 'null';
ALTER TABLE runs ADD COLUMN contract_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN baseline_json JSONB NOT NULL DEFAULT 'null';
ALTER TABLE runs ADD COLUMN final_json JSONB NOT NULL DEFAULT 'null';
ALTER TABLE runs ADD COLUMN verification_outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN verification_digest TEXT NOT NULL DEFAULT '';
