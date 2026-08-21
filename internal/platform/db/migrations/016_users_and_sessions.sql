-- Accounts, so the product can count its users and gate what's worth gating.
--
-- Session tokens rather than JWTs: a token is a random string with a row
-- behind it, which means a session can actually be revoked (delete the row)
-- and there is no signing secret to distribute or rotate. JWTs buy
-- statelessness we have no use for — every request already talks to Postgres.
CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    -- Stored lowercased and trimmed; the unique index is what stops
    -- "User@x.com" and "user@x.com" becoming two accounts.
    email TEXT NOT NULL UNIQUE,
    -- bcrypt hash. Never the password.
    password_hash TEXT NOT NULL,
    is_admin BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS user_sessions (
    -- Opaque random token, also the cookie value.
    token TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);

-- Session lookup happens on every authenticated request; expiry sweeps scan
-- by date.
CREATE INDEX IF NOT EXISTS idx_user_sessions_user ON user_sessions (user_id);
CREATE INDEX IF NOT EXISTS idx_user_sessions_expires ON user_sessions (expires_at);
