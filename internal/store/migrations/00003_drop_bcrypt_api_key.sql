-- +goose Up
-- +goose StatementBegin
-- Drop bcrypt for API keys. Store the key in plaintext (matching the
-- decision we already made for webhook secrets in 00002) so the
-- manager UI can show the full key in a show/hide field instead of a
-- "you can't recover it" reveal flow.
--
-- BREAKING: every existing API key is invalidated. The bcrypt hash is
-- one-way; we cannot recover the plaintext. After this migration runs,
-- bearer auth on /api/v1/* will fail for every existing instance
-- until the operator clicks "Rotate API key" (or PUTs a new key) on
-- each one. The detail page surfaces this with a "no key set" empty
-- field + a "rotate" button.
--
-- Postgres note: SQLite can't `ALTER COLUMN ... DROP NOT NULL`, so
-- this migration rebuilds the table. The Postgres path would be:
--   ALTER TABLE instances RENAME COLUMN api_key_hash TO api_key;
--   ALTER TABLE instances ALTER COLUMN api_key DROP NOT NULL;
--   UPDATE instances SET api_key = NULL;
-- We don't ship a Postgres-specific variant because the dev / CI DB
-- is SQLite; if/when Postgres becomes a production target, write a
-- 00004_postgres_drop_bcrypt.sql alongside this one.

-- 1. Rename the column so the schema name matches the new semantics.
ALTER TABLE instances RENAME COLUMN api_key_hash TO api_key;

-- 2. Drop NOT NULL (requires table rebuild in SQLite).
PRAGMA foreign_keys = OFF;
CREATE TABLE instances_new (
    id                          TEXT PRIMARY KEY,
    name                        TEXT UNIQUE NOT NULL,
    api_key                     TEXT,
    webhook_url                 TEXT,
    webhook_secret              BLOB,
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
INSERT INTO instances_new
    (id, name, api_key, webhook_url, webhook_secret, status, phone,
     jid, lid, connected_at, last_seen_at, api_key_set_at, created_at,
     updated_at)
SELECT
    id, name, NULL, webhook_url, webhook_secret, status, phone,
    jid, lid, connected_at, last_seen_at, api_key_set_at, created_at,
    updated_at
FROM instances;
DROP TABLE instances;
ALTER TABLE instances_new RENAME TO instances;
CREATE INDEX idx_instances_status ON instances(status);
PRAGMA foreign_keys = ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Best-effort rollback: rename the column back. We cannot recover the
-- bcrypt hashes we discarded in the up migration, so the column will
-- be empty — operators will need to rotate to set new keys.
ALTER TABLE instances RENAME COLUMN api_key TO api_key_hash;
-- +goose StatementEnd
