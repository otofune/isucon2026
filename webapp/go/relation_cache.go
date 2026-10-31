package isureal

import (
	"context"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

const relationCacheTTL = 30 * time.Second

// relationEntry は1人の利用者の友達とブロックの関係。ブロックは両方向を含む。
type relationEntry struct {
	friends []string
	blocked []string
	expires time.Time
}

// relationCache は各台のメモリに関係を持つ。関係を変更する API はコミットの後、全台の
// 無効化が終わってから成功を返すので、成功の後に古い関係は読まれない。読み込み中に
// 無効化された利用者の結果は保存しない。
type relationCache struct {
	mu      sync.Mutex
	entries map[string]*relationEntry
	epochs  map[string]uint64
}

var relations = &relationCache{entries: map[string]*relationEntry{}, epochs: map[string]uint64{}}

func (r *relationCache) invalidate(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		delete(r.entries, id)
		r.epochs[id]++
	}
}

func (r *relationCache) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.entries {
		r.epochs[id]++
	}
	r.entries = map[string]*relationEntry{}
}

// load は指定した利用者の関係を返す。ないものは1回の SQL でまとめて読む。
func (r *relationCache) load(ctx context.Context, db sqlx.QueryerContext, ids []string) (map[string]*relationEntry, error) {
	result := make(map[string]*relationEntry, len(ids))
	missing := make([]string, 0)
	epochs := map[string]uint64{}
	now := time.Now()
	r.mu.Lock()
	for _, id := range ids {
		if _, ok := result[id]; ok {
			continue
		}
		if entry, ok := r.entries[id]; ok && now.Before(entry.expires) {
			result[id] = entry
			continue
		}
		missing = append(missing, id)
		epochs[id] = r.epochs[id]
	}
	r.mu.Unlock()
	if len(missing) == 0 {
		return result, nil
	}
	var rows []struct {
		First  string `db:"first_id"`
		Second string `db:"second_id"`
		Kind   string `db:"kind"`
	}
	if err := sqlx.SelectContext(ctx, db, &rows, `
   SELECT user_low_id AS first_id, user_high_id AS second_id, 'friend' AS kind FROM friendships WHERE user_low_id = ANY($1::varchar[])
   UNION ALL SELECT user_low_id, user_high_id, 'friend' FROM friendships WHERE user_high_id = ANY($1::varchar[])
   UNION ALL SELECT blocker_id, blocked_id, 'block' FROM block_relations WHERE blocker_id = ANY($1::varchar[])
   UNION ALL SELECT blocker_id, blocked_id, 'block' FROM block_relations WHERE blocked_id = ANY($1::varchar[])
  `, missing); err != nil {
		return nil, err
	}
	loaded := make(map[string]*relationEntry, len(missing))
	expires := time.Now().Add(relationCacheTTL)
	for _, id := range missing {
		loaded[id] = &relationEntry{expires: expires}
	}
	// 両端が対象のとき同じ行が2回返るので、重複を除く。
	seen := make(map[[3]string]bool, len(rows))
	for _, row := range rows {
		key := [3]string{row.First, row.Second, row.Kind}
		if seen[key] {
			continue
		}
		seen[key] = true
		add := func(owner, other string) {
			entry, ok := loaded[owner]
			if !ok {
				return
			}
			if row.Kind == "friend" {
				entry.friends = append(entry.friends, other)
			} else {
				entry.blocked = append(entry.blocked, other)
			}
		}
		add(row.First, row.Second)
		add(row.Second, row.First)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, entry := range loaded {
		result[id] = entry
		if r.epochs[id] == epochs[id] {
			r.entries[id] = entry
		}
	}
	return result, nil
}

// relationsChanged は関係の変更を commit した後に呼ぶ。全台の無効化が終わるまで返らない。
func (h *HandlerContext) relationsChanged(ctx context.Context, ids ...string) error {
	relations.invalidate(ids)
	var wg sync.WaitGroup
	errs := make(chan error, len(regionPeers))
	for region := range regionPeers {
		if region == h.Config.Region {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- h.callPeer(ctx, region, "/_internal/relations/invalidate", internalRelationsRequest{UserIDs: ids})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// applyRelations は viewer の友達・ブロックと、必要なら友達の友達をメモリの関係から作る。
func applyRelations(ctx context.Context, db sqlx.QueryerContext, viewer *viewerContext, full bool) error {
	entries, err := relations.load(ctx, db, []string{viewer.ID})
	if err != nil {
		return err
	}
	entry := entries[viewer.ID]
	viewer.Blocked = make(map[string]bool, len(entry.blocked))
	for _, id := range entry.blocked {
		viewer.Blocked[id] = true
	}
	for _, id := range entry.friends {
		viewer.Friends[id] = true
	}
	if !full || len(entry.friends) == 0 {
		return nil
	}
	friends, err := relations.load(ctx, db, entry.friends)
	if err != nil {
		return err
	}
	for _, friend := range friends {
		for _, id := range friend.friends {
			viewer.FriendsOfFriends[id] = true
			if id != viewer.ID {
				viewer.MutualFriends[id]++
			}
		}
	}
	return nil
}
