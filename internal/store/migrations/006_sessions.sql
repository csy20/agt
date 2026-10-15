-- A session is a conversation bound to one repository. Each run keeps its own snapshot.

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    repo_path TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX sessions_repo_updated_idx ON sessions (repo_path, updated_at DESC);

CREATE TABLE session_messages (
    session_id TEXT NOT NULL REFERENCES sessions (id),
    seq BIGINT NOT NULL,
    client_message_id TEXT NOT NULL,
    role TEXT NOT NULL,
    kind TEXT NOT NULL,
    content TEXT NOT NULL,
    run_id TEXT REFERENCES runs (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, seq),
    UNIQUE (session_id, client_message_id),
    CHECK (role IN ('user', 'note')),
    CHECK (kind IN ('user', 'pending', 'result'))
);

CREATE INDEX session_messages_run_idx ON session_messages (run_id);

ALTER TABLE runs ADD COLUMN session_id TEXT REFERENCES sessions (id);

CREATE INDEX runs_session_idx ON runs (session_id);
