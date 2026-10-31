package isureal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

const notificationPollInterval = 500 * time.Millisecond

// notificationWaiter は通知を待っている SSE 接続。
type notificationWaiter struct {
	userID      string
	region      string
	connectedAt time.Time
	// 通知が届くと1件送られ、利用者が無効になったときは nil が送られる。
	result chan *getNotificationsResponse
}

// notificationHub は待機中の接続をまとめて確認する。接続が増えても SQL の回数は増えない。
type notificationHub struct {
	mu      sync.Mutex
	waiters map[*notificationWaiter]struct{}
	wake    chan struct{}
}

func newNotificationHub() *notificationHub {
	return &notificationHub{waiters: map[*notificationWaiter]struct{}{}, wake: make(chan struct{}, 1)}
}

func (n *notificationHub) add(waiter *notificationWaiter) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.waiters[waiter] = struct{}{}
}

func (n *notificationHub) remove(waiter *notificationWaiter) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.waiters, waiter)
}

func (n *notificationHub) wakeAll() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *notificationHub) snapshot() []*notificationWaiter {
	n.mu.Lock()
	defer n.mu.Unlock()
	waiters := make([]*notificationWaiter, 0, len(n.waiters))
	for waiter := range n.waiters {
		waiters = append(waiters, waiter)
	}
	return waiters
}

func (n *notificationHub) run(db *sqlx.DB) {
	ticker := time.NewTicker(notificationPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-n.wake:
		}
		// 合図より前に登録された接続は次の確認に含まれる。失敗したら次の周期で確認する。
		if waiters := n.snapshot(); len(waiters) > 0 {
			n.check(db, waiters)
		}
	}
}

func (n *notificationHub) check(db *sqlx.DB, waiters []*notificationWaiter) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := make(map[string]bool, len(waiters))
	userIDs := make([]string, 0, len(waiters))
	oldest := waiters[0].connectedAt
	for _, waiter := range waiters {
		if !seen[waiter.userID] {
			seen[waiter.userID] = true
			userIDs = append(userIDs, waiter.userID)
		}
		if waiter.connectedAt.Before(oldest) {
			oldest = waiter.connectedAt
		}
	}
	var rows []struct {
		UserID          string     `db:"user_id"`
		Region          string     `db:"user_region"`
		ID              *string    `db:"id"`
		PostingWindowID *string    `db:"posting_window_id"`
		WindowRegion    *string    `db:"region"`
		CreatedAt       *time.Time `db:"created_at"`
	}
	// 地域ごとに開いている期間は1つなので、利用者ごとに通知は高々1件になる。
	if err := db.SelectContext(ctx, &rows, `
   SELECT u.id AS user_id, u.region AS user_region, n.id, n.posting_window_id, w.region, n.created_at
   FROM users u
   LEFT JOIN LATERAL (
     SELECT n.id, n.posting_window_id, n.created_at
     FROM notifications n JOIN posting_windows open ON open.id = n.posting_window_id
     WHERE n.user_id = u.id AND n.created_at > $2 AND open.ended_at IS NULL
     ORDER BY n.created_at, n.id LIMIT 1
   ) n ON true
   LEFT JOIN posting_windows w ON w.id = n.posting_window_id
   WHERE u.id = ANY($1::varchar[]) AND u.status = 'active'
  `, userIDs, oldest); err != nil {
		return
	}
	type state struct {
		region       string
		notification *getNotificationsResponse
	}
	states := make(map[string]state, len(rows))
	for _, row := range rows {
		current := state{region: row.Region}
		if row.ID != nil {
			current.notification = &getNotificationsResponse{ID: *row.ID, PostingWindowID: *row.PostingWindowID, Region: *row.WindowRegion, CreatedAt: *row.CreatedAt}
		}
		states[row.UserID] = current
	}
	for _, waiter := range waiters {
		current, ok := states[waiter.userID]
		switch {
		case !ok || current.region != waiter.region:
			n.finish(waiter, nil)
		case current.notification != nil && current.notification.CreatedAt.After(waiter.connectedAt):
			n.finish(waiter, current.notification)
		}
	}
}

func (n *notificationHub) finish(waiter *notificationWaiter, notification *getNotificationsResponse) {
	n.remove(waiter)
	waiter.result <- notification
}

type getNotificationsResponse struct {
	ID              string    `json:"id" db:"id"`
	PostingWindowID string    `json:"postingWindowId" db:"posting_window_id"`
	Region          string    `json:"region" db:"region"`
	CreatedAt       time.Time `json:"createdAt" db:"created_at"`
}

func getNotificationsHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		session := GetSession(c)
		userID := session.UserID
		var connectedAt time.Time
		if err := h.DB.GetContext(ctx, &connectedAt, `SELECT clock_timestamp()`); err != nil {
			return err
		}
		response := c.Response()
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		controller := http.NewResponseController(response)
		if err := controller.Flush(); err != nil {
			return nil
		}
		waiter := &notificationWaiter{userID: userID, region: session.Region, connectedAt: connectedAt, result: make(chan *getNotificationsResponse, 1)}
		h.NotificationWake.add(waiter)
		defer h.NotificationWake.remove(waiter)
		select {
		case <-ctx.Done():
			return nil
		case notification := <-waiter.result:
			if notification == nil {
				return nil
			}
			data, err := json.Marshal(notification)
			if err != nil {
				c.Logger().Error("failed to encode notification", "error", err)
				return nil
			}
			if _, err := fmt.Fprintf(response, "data: %s\n\n", data); err != nil {
				return nil
			}
			_ = controller.Flush()
			return nil
		}
	}
}
