package isureal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const sessionCacheTTL = 30 * time.Second
const sessionCacheLimit = 200_000
const peerTimeout = 3 * time.Second

// 各リージョンのアプリの内部 API。公開側の nginx は /api/ しか転送しない。
var regionPeers = map[string]string{
	"asia":    "http://192.168.1.10:8080",
	"europe":  "http://192.168.2.10:8080",
	"america": "http://192.168.3.10:8080",
}

var peerClient = &http.Client{
	Timeout:   peerTimeout,
	Transport: &http.Transport{MaxIdleConnsPerHost: 32},
}

type cachedSession struct {
	session *Session
	expires time.Time
}

// sessionCache は自リージョンの利用者のセッションだけを持つ。所属地域の台以外で
// ログイン・ログアウト・地域変更が起きたときは、完了前にその台へ同期で無効化を伝える。
type sessionCache struct {
	mu         sync.Mutex
	generation uint64
	byToken    map[string]cachedSession
	tokenByID  map[string]string
}

func newSessionCache() *sessionCache {
	return &sessionCache{byToken: map[string]cachedSession{}, tokenByID: map[string]string{}}
}

func (c *sessionCache) get(tokenHash string) (*Session, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.byToken[tokenHash]
	if ok && time.Now().Before(entry.expires) {
		return entry.session, c.generation
	}
	return nil, c.generation
}

// put は DB を読み始めた時点から無効化がなかったときだけ保存する。
func (c *sessionCache) put(generation uint64, session *Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.generation {
		return
	}
	if len(c.byToken) >= sessionCacheLimit {
		c.byToken = map[string]cachedSession{}
		c.tokenByID = map[string]string{}
	}
	if previous, ok := c.tokenByID[session.UserID]; ok {
		delete(c.byToken, previous)
	}
	c.tokenByID[session.UserID] = session.TokenHash
	c.byToken[session.TokenHash] = cachedSession{session: session, expires: time.Now().Add(sessionCacheTTL)}
}

func (c *sessionCache) invalidateUser(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	if token, ok := c.tokenByID[userID]; ok {
		delete(c.byToken, token)
		delete(c.tokenByID, userID)
	}
}

func (c *sessionCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.byToken = map[string]cachedSession{}
	c.tokenByID = map[string]string{}
}

// sessionChanged はセッションの変更を commit した後に呼ぶ。
func (h *HandlerContext) sessionChanged(ctx context.Context, userID, homeRegion string) error {
	h.Sessions.invalidateUser(userID)
	if homeRegion == h.Config.Region {
		return nil
	}
	return h.callPeer(ctx, homeRegion, "/_internal/sessions/invalidate", internalInvalidateRequest{UserID: userID})
}

func (h *HandlerContext) callPeer(ctx context.Context, region, path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, regionPeers[region]+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(internalTokenHeader, h.Config.DBPassword)
	response, err := peerClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("peer %s%s: status %d", region, path, response.StatusCode)
	}
	return nil
}

// resetPeers は初期化の後、他の台のキャッシュも空にする。
func (h *HandlerContext) resetPeers(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(regionPeers))
	for region := range regionPeers {
		if region == h.Config.Region {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- h.callPeer(ctx, region, "/_internal/reset", struct{}{})
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
