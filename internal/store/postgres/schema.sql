CREATE TABLE IF NOT EXISTS accounts (
    id TEXT PRIMARY KEY,
    login TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_id)
);

CREATE TABLE IF NOT EXISTS hosts (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id),
    name TEXT NOT NULL DEFAULT '',
    ed25519_pub BYTEA NOT NULL,
    node_id TEXT NOT NULL DEFAULT '',
    bundle_id TEXT NOT NULL DEFAULT '',
    banned_at TIMESTAMPTZ,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS hosts_account_id_idx ON hosts (account_id);

CREATE TABLE IF NOT EXISTS device_flow_codes (
    device_code TEXT PRIMARY KEY,
    user_code TEXT NOT NULL UNIQUE,
    account_id TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    interval_s INT NOT NULL DEFAULT 5,
    approved BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS cli_tokens (
    token_hash TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS usage_counters (
    account_id TEXT PRIMARY KEY REFERENCES accounts(id),
    month TEXT NOT NULL,
    bytes BIGINT NOT NULL DEFAULT 0,
    pushes_day DATE,
    pushes INT NOT NULL DEFAULT 0
);
