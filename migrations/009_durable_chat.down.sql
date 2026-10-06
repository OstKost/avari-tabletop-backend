DROP TABLE IF EXISTS ai_reports;
DROP TABLE IF EXISTS chat_events;
DROP TABLE IF EXISTS chat_request_keys;
-- Keep message text/history through schema rollback.
ALTER TABLE chat_messages DROP COLUMN IF EXISTS request_id;
ALTER TABLE chat_messages DROP COLUMN IF EXISTS sequence;
DROP TABLE IF EXISTS chat_requests;
