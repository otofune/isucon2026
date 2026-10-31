-- 実際の検索条件に合う索引。既存 DB にも適用できるよう IF NOT EXISTS にしている。
-- init.sh からも実行するが、初回は realmojis・posts の構築に時間がかかるので、
-- ベンチマーク前に手動で適用しておく。

CREATE UNIQUE INDEX IF NOT EXISTS users_username ON users(username);

-- 画像の有効性確認
CREATE INDEX IF NOT EXISTS posts_image_id_active ON posts(image_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS realmojis_image_id_active ON realmojis(image_id) WHERE replaced_at IS NULL AND deleted_at IS NULL;

-- 関係の逆方向検索
CREATE INDEX IF NOT EXISTS friendships_user_high_id ON friendships(user_high_id);
CREATE INDEX IF NOT EXISTS follows_followee_id ON follows(followee_id);
CREATE INDEX IF NOT EXISTS block_relations_blocked_id ON block_relations(blocked_id);
CREATE INDEX IF NOT EXISTS friend_requests_pending_requester ON friend_requests(requester_id, recipient_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS friend_requests_pending_recipient ON friend_requests(recipient_id, requester_id) WHERE status = 'pending';

-- 認証
CREATE INDEX IF NOT EXISTS login_sessions_token_hash_active ON login_sessions(token_hash) WHERE active;
