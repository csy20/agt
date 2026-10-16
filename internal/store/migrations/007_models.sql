-- A run records the configuration it was created with. Later selection does not rewrite it.

ALTER TABLE runs ADD COLUMN model_config TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN model_name TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN model_endpoint TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN input_per_million BIGINT NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN output_per_million BIGINT NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN pricing_recorded BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE runs ADD COLUMN credential_env TEXT NOT NULL DEFAULT '';

ALTER TABLE sessions ADD COLUMN model_config TEXT NOT NULL DEFAULT '';
