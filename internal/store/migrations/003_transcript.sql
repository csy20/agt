CREATE TABLE model_messages (
    run_id TEXT NOT NULL REFERENCES runs (id),
    seq BIGINT NOT NULL,
    contract TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT '',
    arguments TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, seq)
);
