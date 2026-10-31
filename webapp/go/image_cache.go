package isureal

import (
	"container/list"
	"context"
	"sync"

	"github.com/jmoiron/sqlx"
)

const imageCacheMaxBytes = 1 << 30

// cachedImage は画像の不変な情報だけを持つ。画像 ID は再利用されないので、
// 別の台の初期化や削除で残ったエントリが返ることはない。有効性は呼び出し側が DB で確認する。
type cachedImage struct {
	ID            string `db:"id"`
	OwnerUserID   string `db:"owner_user_id"`
	SecretKeyHash string `db:"secret_key_hash"`
	MediaType     string `db:"media_type"`
	Data          []byte `db:"data"`
}

type imageCache struct {
	mu       sync.Mutex
	maxBytes int
	bytes    int
	entries  map[string]*list.Element
	recent   *list.List
}

func newImageCache(maxBytes int) *imageCache {
	return &imageCache{maxBytes: maxBytes, entries: map[string]*list.Element{}, recent: list.New()}
}

func (c *imageCache) load(ctx context.Context, db sqlx.QueryerContext, id string) (*cachedImage, error) {
	if image := c.get(id); image != nil {
		return image, nil
	}
	image := &cachedImage{}
	if err := sqlx.GetContext(ctx, db, image, `
   SELECT i.id, i.owner_user_id, u.secret_key_hash, i.media_type, i.data
   FROM image_objects i JOIN users u ON u.id = i.owner_user_id
   WHERE i.id = $1
  `, id); err != nil {
		return nil, err
	}
	c.put(image)
	return image, nil
}

func (c *imageCache) get(id string) *cachedImage {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[id]
	if !ok {
		return nil
	}
	c.recent.MoveToFront(element)
	return element.Value.(*cachedImage)
}

func (c *imageCache) put(image *cachedImage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[image.ID]; ok {
		return
	}
	c.entries[image.ID] = c.recent.PushFront(image)
	c.bytes += len(image.Data)
	for c.bytes > c.maxBytes {
		oldest := c.recent.Back()
		evicted := c.recent.Remove(oldest).(*cachedImage)
		delete(c.entries, evicted.ID)
		c.bytes -= len(evicted.Data)
	}
}

func (c *imageCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bytes = 0
	c.entries = map[string]*list.Element{}
	c.recent.Init()
}
