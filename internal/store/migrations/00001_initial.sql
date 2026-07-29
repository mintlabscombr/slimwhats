-- +goose Up
-- +goose StatementBegin
CREATE TABLE instances (
    id                          TEXT PRIMARY KEY,
    name                        TEXT UNIQUE NOT NULL,
    api_key_hash                TEXT NOT NULL,
    webhook_url                 TEXT,
    webhook_secret_encrypted    BLOB,
    status                      TEXT NOT NULL,
    phone                       TEXT,
    jid                         TEXT,
    lid                         TEXT,
    connected_at                TIMESTAMP,
    last_seen_at                TIMESTAMP,
    api_key_set_at              TIMESTAMP,
    created_at                  TIMESTAMP NOT NULL,
    updated_at                  TIMESTAMP NOT NULL
);
CREATE INDEX idx_instances_status ON instances(status);

CREATE TABLE webhook_deliveries (
    id                  TEXT PRIMARY KEY,
    instance_id         TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    event_type          TEXT NOT NULL,
    payload             TEXT NOT NULL,
    status              TEXT NOT NULL,
    attempts            INTEGER NOT NULL DEFAULT 0,
    last_status_code    INTEGER,
    last_error          TEXT,
    created_at          TIMESTAMP NOT NULL,
    updated_at          TIMESTAMP NOT NULL
);
CREATE INDEX idx_webhook_deliveries_instance ON webhook_deliveries(instance_id, created_at DESC);
CREATE INDEX idx_webhook_deliveries_status ON webhook_deliveries(status);

CREATE TABLE instance_logs (
    id          TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    timestamp   TIMESTAMP NOT NULL,
    level       TEXT NOT NULL,
    category    TEXT NOT NULL,
    message     TEXT NOT NULL,
    data        TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_instance_logs_instance_ts ON instance_logs(instance_id, timestamp DESC);

CREATE TABLE admin_sessions (
    id              TEXT PRIMARY KEY,
    username        TEXT NOT NULL,
    created_at      TIMESTAMP NOT NULL,
    expires_at      TIMESTAMP NOT NULL,
    last_seen_at    TIMESTAMP NOT NULL
);
CREATE INDEX idx_admin_sessions_expires ON admin_sessions(expires_at);

CREATE TABLE admin_actions (
    id              TEXT PRIMARY KEY,
    timestamp       TIMESTAMP NOT NULL,
    username        TEXT NOT NULL,
    action          TEXT NOT NULL,
    target_type     TEXT,
    target_id       TEXT,
    source_ip       TEXT NOT NULL,
    user_agent      TEXT,
    data            TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_admin_actions_timestamp ON admin_actions(timestamp DESC);
CREATE INDEX idx_admin_actions_action ON admin_actions(action);
CREATE INDEX idx_admin_actions_target ON admin_actions(target_type, target_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS admin_actions;
DROP TABLE IF EXISTS admin_sessions;
DROP TABLE IF EXISTS instance_logs;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS instances;
-- +goose StatementEnd
