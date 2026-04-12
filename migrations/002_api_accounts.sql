CREATE TABLE IF NOT EXISTS api_accounts (
    account_id      UUID PRIMARY KEY,
    email           TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL DEFAULT '',
    tier            TEXT NOT NULL,
    api_key_hash    TEXT NOT NULL UNIQUE,
    api_key_prefix  TEXT NOT NULL,
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_accounts_api_key_hash
    ON api_accounts (api_key_hash);