package isureal

import (
	"container/list"
	"context"
	"sync"

	"github.com/jmoiron/sqlx"
)

const imageCacheMaxBytes = 768 << 20

// 各アプリ台の PostgreSQL には初期データの画像が入っている。不変なので読み取り専用の
// ストアとして使い、地域間の転送と共有 DB のディスク読み込みを避ける。書き込みはしない。
// cachedImage は画像の不変な情報だけを持つ。画像 ID は再利用されないので、
// 別の台の初期化や削除で残ったエントリが返ることはない。有効性は呼び出し側が DB で確認する。
type cachedImage struct {
	ID            string `db:"id"`
	OwnerUserID   string `db:"owner_user_id"`
	SecretKeyHash string `db:"secret_key_hash"`
	MediaType     string `db:"media_type"`
	Data          []byte `db:"data"`
	// 共有 DB のメタ情報だけが持つ。本体が DB にない画像の配信元リージョン。
	Origin *string `db:"origin"`
}

type imageCache struct {
	mu            sync.Mutex
	local         *sqlx.DB
	region        string
	internalToken string
	maxBytes      int
	bytes         int
	entries       map[string]*list.Element
	recent        *list.List
}

func newImageCache(local *sqlx.DB, config *Config, maxBytes int) *imageCache {
	return &imageCache{local: local, region: config.Region, internalToken: config.DBPassword, maxBytes: maxBytes, entries: map[string]*list.Element{}, recent: list.New()}
}

const localImageSelect = `
   SELECT i.id, i.owner_user_id, u.secret_key_hash, i.media_type, i.data
   FROM image_objects i JOIN users u ON u.id = i.owner_user_id
  `

const sharedImageSelect = `
   SELECT i.id, i.owner_user_id, u.secret_key_hash, i.media_type, i.data, i.origin
   FROM image_objects i JOIN users u ON u.id = i.owner_user_id
  `

// load はメモリ、ローカルの初期画像、共有 DB の順に探す。
func (c *imageCache) load(ctx context.Context, db sqlx.QueryerContext, id string) (*cachedImage, error) {
	if image := c.get(id); image != nil {
		return image, nil
	}
	image := &cachedImage{}
	err := sqlx.GetContext(ctx, c.local, image, localImageSelect+`WHERE i.id = $1`, id)
	if err != nil {
		image = &cachedImage{}
		if err := sqlx.GetContext(ctx, db, image, sharedImageSelect+`WHERE i.id = $1`, id); err != nil {
			return nil, err
		}
		if err := c.fetchImageData(ctx, image); err != nil {
			return nil, err
		}
	}
	c.put(image)
	return image, nil
}

func (c *imageCache) loadMany(ctx context.Context, db sqlx.QueryerContext, ids []string) (map[string]*cachedImage, error) {
	images := make(map[string]*cachedImage, len(ids))
	missing := make([]string, 0)
	for _, id := range ids {
		if image := c.get(id); image != nil {
			images[id] = image
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return images, nil
	}
	missing = c.selectLocal(ctx, images, missing)
	if len(missing) == 0 {
		return images, nil
	}
	var loaded []*cachedImage
	if err := sqlx.SelectContext(ctx, db, &loaded, sharedImageSelect+`WHERE i.id = ANY($1::varchar[])`, missing); err != nil {
		return nil, err
	}
	for _, image := range loaded {
		if err := c.fetchImageData(ctx, image); err != nil {
			return nil, err
		}
		images[image.ID] = image
		c.put(image)
	}
	return images, nil
}

// selectLocal はローカルにある画像を images に入れ、見つからなかった ID を返す。
// ローカルの DB が使えないときは共有 DB に任せる。
func (c *imageCache) selectLocal(ctx context.Context, images map[string]*cachedImage, ids []string) []string {
	var loaded []*cachedImage
	if err := sqlx.SelectContext(ctx, c.local, &loaded, localImageSelect+`WHERE i.id = ANY($1::varchar[])`, ids); err != nil {
		return ids
	}
	for _, image := range loaded {
		images[image.ID] = image
		c.put(image)
	}
	missing := make([]string, 0)
	for _, id := range ids {
		if _, ok := images[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
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
