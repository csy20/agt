ALTER TABLE runs ADD COLUMN interaction TEXT NOT NULL DEFAULT 'agent';

ALTER TABLE runs ADD CONSTRAINT runs_interaction_chk CHECK (interaction IN ('agent', 'ask', 'plan'));
