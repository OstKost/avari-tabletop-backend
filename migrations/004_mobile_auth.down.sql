DROP TABLE password_reset_tokens;
DROP TABLE mobile_refresh_tokens;
DROP TABLE mobile_sessions;
ALTER TABLE users DROP COLUMN auth_version;
