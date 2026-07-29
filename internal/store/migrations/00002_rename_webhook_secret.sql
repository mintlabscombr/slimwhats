-- +goose Up
-- +goose StatementBegin
-- v1 hotfix: webhook secret is no longer encrypted at rest. The
-- column name "webhook_secret_encrypted" is now a lie (the bytes
-- inside are plaintext), so rename it to "webhook_secret" to keep
-- the schema honest. The column type stays BLOB; data is unchanged.
--
-- Both SQLite (3.25+) and Postgres support RENAME COLUMN; the
-- migration is idempotent in practice (goose tracks the version).
ALTER TABLE instances RENAME COLUMN webhook_secret_encrypted TO webhook_secret;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE instances RENAME COLUMN webhook_secret TO webhook_secret_encrypted;
-- +goose StatementEnd
