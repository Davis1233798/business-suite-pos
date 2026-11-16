CREATE TABLE IF NOT EXISTS pos_consumer_accounts (
 id TEXT PRIMARY KEY,
 organization_id TEXT NOT NULL REFERENCES organizations(id),
 normalized_email TEXT NOT NULL,
 password_hash TEXT NOT NULL,
 verified_at INTEGER,
 disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0,1)),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(organization_id, normalized_email),
 UNIQUE(id, organization_id),
 CHECK(verified_at IS NULL OR length(password_hash) > 0)
);
CREATE INDEX IF NOT EXISTS pos_consumer_accounts_organization ON pos_consumer_accounts(organization_id, id);

CREATE TABLE IF NOT EXISTS pos_consumer_verification_challenges (
 token_hash TEXT PRIMARY KEY,
 account_id TEXT NOT NULL,
 organization_id TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
 consumed_at INTEGER,
 created_at INTEGER NOT NULL,
 FOREIGN KEY(account_id, organization_id) REFERENCES pos_consumer_accounts(id, organization_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS pos_consumer_verification_challenges_account ON pos_consumer_verification_challenges(organization_id, account_id, expires_at);

CREATE TABLE IF NOT EXISTS pos_consumer_sessions (
 token_hash TEXT PRIMARY KEY,
 account_id TEXT NOT NULL,
 organization_id TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 revoked_at INTEGER,
 created_at INTEGER NOT NULL,
 FOREIGN KEY(account_id, organization_id) REFERENCES pos_consumer_accounts(id, organization_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS pos_consumer_sessions_account ON pos_consumer_sessions(organization_id, account_id, expires_at);
