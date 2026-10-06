CREATE SEQUENCE collection_version_seq;
ALTER TABLE collections ADD COLUMN version BIGINT NOT NULL DEFAULT nextval('collection_version_seq');
ALTER TABLE collections ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
UPDATE collections SET updated_at = added_at;
CREATE FUNCTION bump_collection_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.version := nextval('collection_version_seq');
  NEW.updated_at := NOW();
  RETURN NEW;
END $$;
CREATE TRIGGER collection_version_update BEFORE UPDATE ON collections
FOR EACH ROW EXECUTE FUNCTION bump_collection_version();
ALTER TABLE games ADD COLUMN name_fold TEXT NOT NULL DEFAULT '';
UPDATE games SET name_fold = translate(lower(name), 'АБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯ', 'абвгдеёжзийклмнопрстуфхцчшщъыьэюя');
CREATE INDEX collections_user_order ON collections(user_id, added_at DESC, game_id);
CREATE TABLE collection_add_replays (
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 key UUID NOT NULL, body_hash TEXT NOT NULL, status INTEGER NOT NULL,
 request_id UUID NOT NULL, response JSONB NOT NULL, expires_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(user_id,key)
);
CREATE TABLE collection_snapshots (
 id UUID PRIMARY KEY, user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 captured_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), expires_at TIMESTAMPTZ NOT NULL,
 items JSONB NOT NULL
);
CREATE INDEX collection_snapshots_user_expiry ON collection_snapshots(user_id,expires_at);
