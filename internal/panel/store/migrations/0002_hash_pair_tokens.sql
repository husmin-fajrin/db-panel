-- Pairing tokens are stored as their SHA-256 (hex) from now on, never in
-- plain text. Existing plaintext tokens cannot be hashed in SQL, so they are
-- dropped: an operator creates a new token from the server page.
DELETE FROM pair_tokens;
ALTER TABLE pair_tokens RENAME COLUMN token TO token_hash;
