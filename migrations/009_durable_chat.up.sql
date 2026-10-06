CREATE TABLE chat_requests (
 id UUID PRIMARY KEY,
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 game_id UUID NOT NULL REFERENCES games(id) ON DELETE CASCADE,
 client_request_id UUID NOT NULL,
 body_hash TEXT NOT NULL,
 input_message TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','running','completed','cancelled','failed')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 started_at TIMESTAMPTZ,
 deadline_at TIMESTAMPTZ,
 completed_at TIMESTAMPTZ,
 result JSONB,
 error_code TEXT,
 last_sequence BIGINT NOT NULL DEFAULT 0,
 event_bytes INTEGER NOT NULL DEFAULT 0,
 deleted_at TIMESTAMPTZ,
 UNIQUE(user_id,game_id,client_request_id),
 CHECK((status IN ('completed','cancelled','failed')) = (completed_at IS NOT NULL)),
 CHECK((status='completed' AND deleted_at IS NULL) = (result IS NOT NULL))
);
CREATE UNIQUE INDEX chat_requests_active ON chat_requests(user_id,game_id) WHERE status IN ('pending','running');
CREATE INDEX chat_requests_queue ON chat_requests(created_at) WHERE status='pending';
CREATE INDEX chat_requests_quota ON chat_requests(user_id,created_at);
CREATE TABLE chat_request_keys (
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 key UUID NOT NULL,
 request_id UUID NOT NULL REFERENCES chat_requests(id) ON DELETE CASCADE,
 PRIMARY KEY(user_id,key)
);
CREATE TABLE chat_events (
 request_id UUID NOT NULL REFERENCES chat_requests(id) ON DELETE CASCADE,
 sequence BIGINT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('started','delta','completed','failed','cancelled')),
 payload JSONB NOT NULL,
 terminal BOOLEAN NOT NULL DEFAULT false,
 PRIMARY KEY(request_id,sequence)
);
CREATE UNIQUE INDEX chat_events_terminal ON chat_events(request_id) WHERE terminal;
ALTER TABLE chat_messages ADD COLUMN request_id UUID REFERENCES chat_requests(id) ON DELETE CASCADE;
ALTER TABLE chat_messages ADD COLUMN sequence BIGSERIAL;
CREATE UNIQUE INDEX chat_messages_pair ON chat_messages(request_id,role) WHERE request_id IS NOT NULL;
CREATE INDEX chat_messages_tail ON chat_messages(user_id,game_id,sequence DESC);
CREATE TABLE ai_reports (
 id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 request_id UUID NOT NULL REFERENCES chat_requests(id) ON DELETE CASCADE,
 reason TEXT NOT NULL CHECK(reason IN ('incorrect','unsafe','other')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW()+INTERVAL '30 days',
 UNIQUE(user_id,request_id,reason)
);
