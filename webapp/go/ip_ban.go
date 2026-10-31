package isureal

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

const ipBanReloadInterval = 200 * time.Millisecond

type IPBanList struct {
	snapshot atomic.Pointer[ipBanSnapshot]
}

type ipBanSnapshot struct {
	bans map[string]time.Time
}

type ipBan struct {
	IP          string    `db:"ip"`
	BannedUntil time.Time `db:"banned_until"`
}

// start はリクエストを待たせないよう、BAN 一覧を別の goroutine で更新する。
func (l *IPBanList) start(db *sqlx.DB) {
	if loaded, err := loadIPBans(context.Background(), db); err == nil {
		l.snapshot.Store(loaded)
	}
	go func() {
		for range time.Tick(ipBanReloadInterval) {
			if loaded, err := loadIPBans(context.Background(), db); err == nil {
				l.snapshot.Store(loaded)
			}
		}
	}()
}

func (l *IPBanList) Banned(ip string) bool {
	snapshot := l.snapshot.Load()
	if snapshot == nil {
		return false
	}
	until, ok := snapshot.bans[ip]
	return ok && time.Now().Before(until)
}

func loadIPBans(ctx context.Context, db *sqlx.DB) (*ipBanSnapshot, error) {
	var rows []ipBan
	if err := db.SelectContext(ctx, &rows, `SELECT ip, banned_until FROM ip_bans WHERE banned_until > clock_timestamp()`); err != nil {
		return nil, err
	}
	bans := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		bans[row.IP] = row.BannedUntil
	}
	return &ipBanSnapshot{bans: bans}, nil
}

func extractClientIP(req *http.Request) string {
	if ip := req.Header.Get("X-Client-IP"); ip != "" {
		return ip
	}
	return echo.ExtractIPDirect()(req)
}

func IPBanMiddleware(h *HandlerContext) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if h.IPBans.Banned(c.RealIP()) {
				return NewAPIError(http.StatusForbidden, "ip_banned")
			}
			return next(c)
		}
	}
}
