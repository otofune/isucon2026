-- トランザクション全体を1往復で実行し、地域間の遅延の間ロックを持たないための関数。
-- 各文は READ COMMITTED で新しいスナップショットを取るので、ロックの後の読み取りは
-- 先行するトランザクションの結果を見る。

DROP FUNCTION IF EXISTS isu_create_post(text, text, text, text, text, text, numeric, numeric, text, text);
CREATE FUNCTION isu_create_post(
  p_user text, p_post text, p_image text, p_caption text, p_visibility text,
  p_location_kind text, p_latitude numeric, p_longitude numeric, p_locality text, p_origin text
) RETURNS TABLE (
  result text, id text, username text, secret_key_hash text, display_name text, bio text,
  profile_image_id text, region text, account_type text, status text,
  window_id text, posted_at timestamptz, on_time_deadline timestamptz
) LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
  u users%ROWTYPE;
  w posting_windows%ROWTYPE;
  active_count int;
  deleted_count int;
  now_at timestamptz;
BEGIN
  SELECT * INTO u FROM users WHERE users.id = p_user AND users.status = 'active' FOR NO KEY UPDATE;
  IF NOT FOUND THEN
    RETURN QUERY SELECT 'no_user'::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, NULL::timestamptz, NULL::timestamptz;
    RETURN;
  END IF;
  -- 投稿同士は共有し、期間の切り替えとだけ競合させる。切り替わると0件になるので読み直す。
  FOR attempt IN 1..2 LOOP
    SELECT * INTO w FROM posting_windows pw WHERE pw.region = u.region AND pw.ended_at IS NULL FOR SHARE;
    EXIT WHEN FOUND;
  END LOOP;
  IF w.id IS NULL THEN
    RETURN QUERY SELECT 'unavailable'::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, NULL::timestamptz, NULL::timestamptz;
    RETURN;
  END IF;
  SELECT count(*) FILTER (WHERE p.deleted_at IS NULL), count(*) FILTER (WHERE p.deleted_at IS NOT NULL)
    INTO active_count, deleted_count
    FROM posts p JOIN posting_windows posted ON posted.id = p.posting_window_id
    WHERE p.author_id = p_user AND posted.local_date = w.local_date;
  now_at := date_trunc('microseconds', clock_timestamp());
  IF active_count <> 0 OR deleted_count >= 2 OR now_at < w.starts_at THEN
    RETURN QUERY SELECT 'unavailable'::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, NULL::timestamptz, NULL::timestamptz;
    RETURN;
  END IF;
  INSERT INTO image_objects (id, owner_user_id, data, media_type, created_at, origin)
    VALUES (p_image, p_user, '\x', 'image/jpeg', now_at, p_origin);
  INSERT INTO posts (id, author_id, posting_window_id, image_id, caption, visibility, location_kind, latitude, longitude, locality, posted_at)
    VALUES (p_post, p_user, w.id, p_image, p_caption, p_visibility, p_location_kind, p_latitude, p_longitude, p_locality, now_at);
  RETURN QUERY SELECT 'ok'::text, u.id::text, u.username::text, u.secret_key_hash, u.display_name, u.bio, u.profile_image_id::text, u.region, u.account_type, u.status, w.id::text, now_at, w.on_time_deadline;
END $$;

DROP FUNCTION IF EXISTS isu_login(text, text, text, text);
CREATE FUNCTION isu_login(p_username text, p_secret_hash text, p_session text, p_token_hash text)
RETURNS TABLE (
  id text, username text, secret_key_hash text, display_name text, bio text,
  profile_image_id text, region text, account_type text, status text
) LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
  u users%ROWTYPE;
BEGIN
  -- 同じ利用者の同時ログインをこの行ロックで直列化する。
  SELECT * INTO u FROM users WHERE users.username = p_username FOR NO KEY UPDATE;
  IF NOT FOUND OR u.status <> 'active' OR u.secret_key_hash <> p_secret_hash THEN
    RETURN;
  END IF;
  UPDATE login_sessions SET active = false, invalidated_at = clock_timestamp(), reason = 'replaced'
    WHERE user_id = u.id AND active = true;
  INSERT INTO login_sessions (id, user_id, token_hash) VALUES (p_session, u.id, p_token_hash);
  RETURN QUERY SELECT u.id::text, u.username::text, u.secret_key_hash, u.display_name, u.bio, u.profile_image_id::text, u.region, u.account_type, u.status;
END $$;

-- 関係変更の共通ロック。同じ2人だけを直列化し、利用者の行は共有ロックにする。
DROP FUNCTION IF EXISTS isu_lock_pair(text, text);
CREATE FUNCTION isu_lock_pair(p_first text, p_second text) RETURNS boolean LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
  locked int;
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(
    CASE WHEN p_first COLLATE "C" < p_second COLLATE "C" THEN p_first || ':' || p_second ELSE p_second || ':' || p_first END, 0));
  SELECT count(*) INTO locked FROM (
    SELECT 1 FROM users u WHERE u.id IN (p_first, p_second) AND u.status = 'active' ORDER BY u.id COLLATE "C" FOR SHARE
  ) rows;
  RETURN locked = 2;
END $$;

DROP FUNCTION IF EXISTS isu_create_friend_request(text, text, text, text);
CREATE FUNCTION isu_create_friend_request(p_actor text, p_target text, p_request text, p_friendship text)
RETURNS TABLE (result text, id text, status text, updated_at timestamptz) LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
  pending friend_requests%ROWTYPE;
  low text;
  high text;
BEGIN
  IF NOT isu_lock_pair(p_actor, p_target) THEN
    RETURN QUERY SELECT 'not_found'::text, ''::text, ''::text, NULL::timestamptz;
    RETURN;
  END IF;
  IF p_actor COLLATE "C" < p_target COLLATE "C" THEN low := p_actor; high := p_target; ELSE low := p_target; high := p_actor; END IF;
  IF EXISTS(SELECT 1 FROM block_relations b WHERE (b.blocker_id = p_actor AND b.blocked_id = p_target) OR (b.blocker_id = p_target AND b.blocked_id = p_actor))
     OR EXISTS(SELECT 1 FROM friendships f WHERE f.user_low_id = low AND f.user_high_id = high) THEN
    RETURN QUERY SELECT 'conflict'::text, ''::text, ''::text, NULL::timestamptz;
    RETURN;
  END IF;
  SELECT * INTO pending FROM friend_requests r WHERE r.status = 'pending'
    AND ((r.requester_id = p_actor AND r.recipient_id = p_target) OR (r.requester_id = p_target AND r.recipient_id = p_actor))
    LIMIT 1;
  IF FOUND THEN
    IF pending.requester_id = p_actor THEN
      RETURN QUERY SELECT 'conflict'::text, ''::text, ''::text, NULL::timestamptz;
      RETURN;
    END IF;
    INSERT INTO friendships (id, user_low_id, user_high_id, source_request_id) VALUES (p_friendship, low, high, pending.id);
    RETURN QUERY UPDATE friend_requests r SET status = 'accepted', updated_at = clock_timestamp()
      WHERE r.id = pending.id RETURNING 'ok'::text, r.id::text, r.status, r.updated_at;
    RETURN;
  END IF;
  RETURN QUERY INSERT INTO friend_requests AS r (id, requester_id, recipient_id, status, created_at, updated_at)
    VALUES (p_request, p_actor, p_target, 'pending', clock_timestamp(), clock_timestamp())
    RETURNING 'ok'::text, r.id::text, r.status, r.updated_at;
END $$;

DROP FUNCTION IF EXISTS isu_transition_friend_request(text, text, text, text);
CREATE FUNCTION isu_transition_friend_request(p_request text, p_actor text, p_status text, p_friendship text)
RETURNS TABLE (result text, id text, status text, updated_at timestamptz) LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
  request friend_requests%ROWTYPE;
BEGIN
  SELECT * INTO request FROM friend_requests r WHERE r.id = p_request;
  IF NOT FOUND THEN
    RETURN QUERY SELECT 'not_found'::text, ''::text, ''::text, NULL::timestamptz;
    RETURN;
  END IF;
  IF (p_status = 'cancelled' AND p_actor <> request.requester_id) OR (p_status <> 'cancelled' AND p_actor <> request.recipient_id) THEN
    RETURN QUERY SELECT 'forbidden'::text, ''::text, ''::text, NULL::timestamptz;
    RETURN;
  END IF;
  IF NOT isu_lock_pair(request.requester_id, request.recipient_id) THEN
    RETURN QUERY SELECT 'not_found'::text, ''::text, ''::text, NULL::timestamptz;
    RETURN;
  END IF;
  SELECT * INTO request FROM friend_requests r WHERE r.id = p_request;
  IF request.status <> 'pending' THEN
    RETURN QUERY SELECT 'forbidden'::text, ''::text, ''::text, NULL::timestamptz;
    RETURN;
  END IF;
  IF p_status = 'accepted' THEN
    IF EXISTS(SELECT 1 FROM block_relations b WHERE (b.blocker_id = request.requester_id AND b.blocked_id = request.recipient_id) OR (b.blocker_id = request.recipient_id AND b.blocked_id = request.requester_id)) THEN
      RETURN QUERY SELECT 'conflict'::text, ''::text, ''::text, NULL::timestamptz;
      RETURN;
    END IF;
    INSERT INTO friendships (id, user_low_id, user_high_id, source_request_id) VALUES (
      p_friendship,
      CASE WHEN request.requester_id COLLATE "C" < request.recipient_id COLLATE "C" THEN request.requester_id ELSE request.recipient_id END,
      CASE WHEN request.requester_id COLLATE "C" < request.recipient_id COLLATE "C" THEN request.recipient_id ELSE request.requester_id END,
      request.id);
  END IF;
  RETURN QUERY UPDATE friend_requests r SET status = p_status, updated_at = clock_timestamp()
    WHERE r.id = request.id RETURNING 'ok'::text, r.id::text, r.status, r.updated_at;
END $$;

-- post.go の viewerContext.canView と comment.go の canComment と同じ判定。
DROP FUNCTION IF EXISTS isu_interaction_state(text, text);
CREATE FUNCTION isu_interaction_state(p_viewer text, p_post text)
RETURNS TABLE (can_view boolean, can_comment boolean) LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
  v_author text;
  v_visibility text;
  v_ended boolean;
  v_region text;
  v_posted boolean;
  v_friend boolean;
  v_reachable boolean;
  v_view boolean;
BEGIN
  SELECT p.author_id, p.visibility, w.ended_at IS NOT NULL INTO v_author, v_visibility, v_ended
    FROM posts p
    JOIN posting_windows w ON w.id = p.posting_window_id
    JOIN users a ON a.id = p.author_id AND a.status = 'active'
    WHERE p.id = p_post AND p.deleted_at IS NULL;
  IF NOT FOUND THEN
    RETURN QUERY SELECT false, false;
    RETURN;
  END IF;
  SELECT u.region INTO v_region FROM users u WHERE u.id = p_viewer AND u.status = 'active';
  IF NOT FOUND THEN
    RETURN QUERY SELECT false, false;
    RETURN;
  END IF;
  v_posted := EXISTS(
    SELECT 1 FROM posts p
    JOIN posting_windows posted ON posted.id = p.posting_window_id
    JOIN posting_windows current ON current.region = v_region AND current.ended_at IS NULL
                                AND current.local_date = posted.local_date
    WHERE p.author_id = p_viewer AND p.deleted_at IS NULL);
  IF v_author = p_viewer THEN
    RETURN QUERY SELECT true, (NOT v_ended) AND v_posted;
    RETURN;
  END IF;
  IF v_ended OR NOT v_posted OR EXISTS(
    SELECT 1 FROM block_relations b
    WHERE (b.blocker_id = p_viewer AND b.blocked_id = v_author) OR (b.blocker_id = v_author AND b.blocked_id = p_viewer)) THEN
    RETURN QUERY SELECT false, false;
    RETURN;
  END IF;
  v_friend := EXISTS(SELECT 1 FROM friendships f
    WHERE (f.user_low_id = p_viewer AND f.user_high_id = v_author) OR (f.user_low_id = v_author AND f.user_high_id = p_viewer));
  v_reachable := EXISTS(
    WITH viewer_friends AS (
      SELECT f.user_high_id AS id FROM friendships f WHERE f.user_low_id = p_viewer
      UNION ALL SELECT f.user_low_id FROM friendships f WHERE f.user_high_id = p_viewer
    ), author_friends AS (
      SELECT f.user_high_id AS id FROM friendships f WHERE f.user_low_id = v_author
      UNION ALL SELECT f.user_low_id FROM friendships f WHERE f.user_high_id = v_author
    )
    SELECT 1 FROM viewer_friends JOIN author_friends ON author_friends.id = viewer_friends.id);
  v_view := CASE v_visibility
    WHEN 'public' THEN true
    WHEN 'friends' THEN v_friend
    WHEN 'friends_of_friends' THEN v_friend OR v_reachable
    ELSE false END;
  RETURN QUERY SELECT v_view, v_view AND (v_visibility = 'public' OR v_friend);
END $$;

DROP FUNCTION IF EXISTS isu_create_comment(text, text, text, text);
CREATE FUNCTION isu_create_comment(p_actor text, p_post text, p_comment text, p_body text)
RETURNS TABLE (
  result text, created_at timestamptz, id text, username text, secret_key_hash text, display_name text, bio text,
  profile_image_id text, region text, account_type text, status text
) LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
  v_author text;
  v_allowed boolean;
  v_created timestamptz;
  u users%ROWTYPE;
BEGIN
  SELECT p.author_id INTO v_author FROM posts p WHERE p.id = p_post AND p.deleted_at IS NULL;
  IF NOT FOUND THEN
    RETURN QUERY SELECT 'forbidden'::text, NULL::timestamptz, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text;
    RETURN;
  END IF;
  PERFORM 1 FROM users x WHERE x.id IN (p_actor, v_author) ORDER BY x.id COLLATE "C" FOR NO KEY UPDATE;
  SELECT s.can_comment INTO v_allowed FROM isu_interaction_state(p_actor, p_post) s;
  IF NOT v_allowed THEN
    RETURN QUERY SELECT 'forbidden'::text, NULL::timestamptz, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text;
    RETURN;
  END IF;
  INSERT INTO comments (id, post_id, author_id, body, created_at) VALUES (p_comment, p_post, p_actor, p_body, clock_timestamp())
    RETURNING comments.created_at INTO v_created;
  SELECT * INTO u FROM users x WHERE x.id = p_actor;
  RETURN QUERY SELECT 'ok'::text, v_created, u.id::text, u.username::text, u.secret_key_hash, u.display_name, u.bio, u.profile_image_id::text, u.region, u.account_type, u.status;
END $$;

DROP FUNCTION IF EXISTS isu_replace_realmoji(text, text, text, text, text, text);
CREATE FUNCTION isu_replace_realmoji(p_actor text, p_post text, p_realmoji text, p_image text, p_reaction text, p_origin text)
RETURNS TABLE (
  result text, created_at timestamptz, id text, username text, secret_key_hash text, display_name text, bio text,
  profile_image_id text, region text, account_type text, status text
) LANGUAGE plpgsql AS $$
#variable_conflict use_column
DECLARE
  v_author text;
  v_allowed boolean;
  v_created timestamptz;
  u users%ROWTYPE;
BEGIN
  SELECT p.author_id INTO v_author FROM posts p WHERE p.id = p_post AND p.deleted_at IS NULL;
  IF NOT FOUND OR v_author = p_actor THEN
    RETURN QUERY SELECT 'forbidden'::text, NULL::timestamptz, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text;
    RETURN;
  END IF;
  IF NOT isu_lock_pair(p_actor, v_author) THEN
    RETURN QUERY SELECT 'not_found'::text, NULL::timestamptz, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text;
    RETURN;
  END IF;
  SELECT s.can_view INTO v_allowed FROM isu_interaction_state(p_actor, p_post) s;
  IF NOT v_allowed THEN
    RETURN QUERY SELECT 'forbidden'::text, NULL::timestamptz, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text, ''::text;
    RETURN;
  END IF;
  UPDATE realmojis r SET replaced_at = clock_timestamp()
    WHERE r.post_id = p_post AND r.reactor_id = p_actor AND r.replaced_at IS NULL AND r.deleted_at IS NULL;
  INSERT INTO image_objects (id, owner_user_id, data, media_type, origin) VALUES (p_image, p_actor, '\x', 'image/jpeg', p_origin);
  INSERT INTO realmojis (id, post_id, reactor_id, image_id, reaction_type, created_at)
    VALUES (p_realmoji, p_post, p_actor, p_image, p_reaction, clock_timestamp())
    RETURNING realmojis.created_at INTO v_created;
  SELECT * INTO u FROM users x WHERE x.id = p_actor;
  RETURN QUERY SELECT 'ok'::text, v_created, u.id::text, u.username::text, u.secret_key_hash, u.display_name, u.bio, u.profile_image_id::text, u.region, u.account_type, u.status;
END $$;

-- 検索用の一覧の版。トランザクションの中で上げるので、コミット前に新しい版は見えない。
CREATE TABLE IF NOT EXISTS users_version (id int PRIMARY KEY, version bigint NOT NULL);
INSERT INTO users_version (id, version) VALUES (1, 1) ON CONFLICT (id) DO NOTHING;

DROP TRIGGER IF EXISTS users_version_bump ON users;
DROP FUNCTION IF EXISTS isu_bump_users_version();
CREATE FUNCTION isu_bump_users_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE users_version SET version = version + 1 WHERE id = 1;
  RETURN NULL;
END $$;
CREATE TRIGGER users_version_bump AFTER INSERT OR DELETE OR UPDATE OF username, display_name, status ON users
  FOR EACH STATEMENT EXECUTE FUNCTION isu_bump_users_version();
