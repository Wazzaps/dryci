CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
    email TEXT NOT NULL UNIQUE,
    full_name TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    disabled_at INTEGER,
    superuser BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE api_tokens (
    token TEXT PRIMARY KEY NOT NULL,
    user_id INTEGER NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    expires_at INTEGER,
    disabled_at INTEGER,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE user_usage (
    timestamp INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    user_id INTEGER NOT NULL,
    type INTEGER NOT NULL,
    count INTEGER NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE test_results (
    user_id INTEGER NOT NULL,
    dep_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    accessed_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    node_ids TEXT NOT NULL,
    PRIMARY KEY (user_id, dep_hash),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) WITHOUT ROWID;

INSERT INTO users (full_name, email, superuser) VALUES ('Administrator', 'root@localhost', 1);
