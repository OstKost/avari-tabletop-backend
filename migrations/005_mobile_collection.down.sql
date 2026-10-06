DROP TABLE IF EXISTS collection_snapshots;
DROP TABLE IF EXISTS collection_add_replays;
DROP TRIGGER IF EXISTS collection_version_update ON collections;
DROP FUNCTION IF EXISTS bump_collection_version();
ALTER TABLE collections DROP COLUMN IF EXISTS version, DROP COLUMN IF EXISTS updated_at;
DROP SEQUENCE IF EXISTS collection_version_seq;
DROP INDEX IF EXISTS collections_user_order;
ALTER TABLE games DROP COLUMN IF EXISTS name_fold;
