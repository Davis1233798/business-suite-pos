CREATE TABLE IF NOT EXISTS pos_consumer_pairing_tokens (
 token_hash TEXT PRIMARY KEY,
 account_id TEXT NOT NULL,
 organization_id TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 consumed_sale_id TEXT,
 created_at INTEGER NOT NULL,
 FOREIGN KEY(account_id, organization_id) REFERENCES pos_consumer_accounts(id, organization_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS pos_consumer_pairing_tokens_account ON pos_consumer_pairing_tokens(organization_id, account_id, expires_at);
