package isureal

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type searchUsersRequest struct {
	Q              string `query:"q"`
	BeforeUsername string `query:"beforeUsername"`
	BeforeID       string `query:"beforeId"`
}

type searchUsersResponse struct {
	Items   []relationshipUserResponse `json:"items"`
	HasMore bool                       `json:"hasMore"`
}

func normalizeSearchText(value string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(value)))
}

type directoryEntry struct {
	ID                string `db:"id"`
	Username          string `db:"username"`
	DisplayName       string `db:"display_name"`
	searchUsername    string
	searchDisplayName string
}

type userDirectory struct {
	version   string
	entries   []directoryEntry
	positions map[string]int
}

// directoryCache は検索用の一覧を持つ。版はトリガーが同じトランザクションで上げるので、
// 書き込みの直後でも古い一覧は使わない。一覧が変わっていたら、待っている検索を次の
// 1回の読み直しに束ね、ロックを持ったまま DB を待たない。
type directoryCache struct {
	mu      sync.Mutex
	current *userDirectory
	pending *directoryRefresh
	running bool
}

type directoryRefresh struct {
	done      chan struct{}
	directory *userDirectory
	err       error
}

func (d *directoryCache) load(ctx context.Context, db *sqlx.DB) (*userDirectory, error) {
	var version string
	if err := db.GetContext(ctx, &version, `SELECT version::text FROM users_version WHERE id = 1`); err != nil {
		return nil, err
	}
	d.mu.Lock()
	if d.current != nil && d.current.version == version {
		current := d.current
		d.mu.Unlock()
		return current, nil
	}
	if d.pending == nil {
		d.pending = &directoryRefresh{done: make(chan struct{})}
	}
	refresh := d.pending
	if !d.running {
		d.running = true
		go d.run(db)
	}
	d.mu.Unlock()
	select {
	case <-refresh.done:
		return refresh.directory, refresh.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// run は待っている検索がなくなるまで読み直す。読み直しの開始より前に並んだ検索は、
// その版以降の内容を受け取る。
func (d *directoryCache) run(db *sqlx.DB) {
	for {
		d.mu.Lock()
		refresh := d.pending
		d.pending = nil
		previous := d.current
		if refresh == nil {
			d.running = false
			d.mu.Unlock()
			return
		}
		d.mu.Unlock()
		refresh.directory, refresh.err = fetchDirectory(db, previous)
		if refresh.err == nil {
			d.mu.Lock()
			d.current = refresh.directory
			d.mu.Unlock()
		}
		close(refresh.done)
	}
}

func fetchDirectory(db *sqlx.DB, previous *userDirectory) (*userDirectory, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var rows []struct {
		directoryEntry
		Version string `db:"version"`
	}
	if err := db.SelectContext(ctx, &rows, `SELECT id, username, display_name, (SELECT version::text FROM users_version WHERE id = 1) AS version FROM users WHERE status='active' ORDER BY username, id`); err != nil {
		return nil, err
	}
	directory := &userDirectory{entries: make([]directoryEntry, len(rows)), positions: make(map[string]int, len(rows))}
	for i := range rows {
		entry := rows[i].directoryEntry
		directory.version = rows[i].Version
		if index, ok := positionOf(previous, entry); ok && previous.entries[index].DisplayName == entry.DisplayName {
			entry.searchUsername = previous.entries[index].searchUsername
			entry.searchDisplayName = previous.entries[index].searchDisplayName
		} else {
			entry.searchUsername = normalizeSearchText(entry.Username)
			entry.searchDisplayName = normalizeSearchText(entry.DisplayName)
		}
		directory.entries[i] = entry
		directory.positions[entry.Username+"\x00"+entry.ID] = i
	}
	return directory, nil
}

func positionOf(directory *userDirectory, entry directoryEntry) (int, bool) {
	if directory == nil {
		return 0, false
	}
	index, ok := directory.positions[entry.Username+"\x00"+entry.ID]
	return index, ok
}

func searchUsersHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input searchUsersRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if !validUserText(input.Q, 1, 100) {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}
		if (input.BeforeUsername == "") != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		ctx := c.Request().Context()
		viewer := GetSession(c).UserID
		// 判定に必要な列の一覧は、利用者表が変わっていなければ前回のものを使う。
		directory, err := h.Directory.load(ctx, h.DB)
		if err != nil {
			return err
		}
		start := 0
		if input.BeforeUsername != "" {
			index, ok := directory.positions[input.BeforeUsername+"\x00"+input.BeforeID]
			if !ok {
				// 基準の利用者が一覧にないときは位置を決められないので DB で順序を判定する。
				var after int
				if err := h.DB.GetContext(ctx, &after, `SELECT count(*) FROM users WHERE status='active' AND (username,id)<=($1,$2)`, input.BeforeUsername, input.BeforeID); err != nil {
					return err
				}
				index = after - 1
			}
			start = min(index+1, len(directory.entries))
		}
		blocked, err := getBlockedUserIDs(ctx, h.DB, viewer)
		if err != nil {
			return err
		}
		query := normalizeSearchText(input.Q)
		matchedIDs := make([]string, 0, 51)
		for _, user := range directory.entries[start:] {
			if user.ID == viewer {
				continue
			}
			if !strings.Contains(user.searchUsername, query) && !strings.Contains(user.searchDisplayName, query) {
				continue
			}
			if blocked[user.ID] {
				continue
			}
			matchedIDs = append(matchedIDs, user.ID)
			if len(matchedIDs) == 51 {
				break
			}
		}
		items := make([]relationshipUserResponse, 0, len(matchedIDs))
		if len(matchedIDs) > 0 {
			statement, args, err := sqlx.In(`SELECT `+strings.Join(userColumns, ", ")+` FROM users WHERE status='active' AND id IN (?) ORDER BY username, id`, matchedIDs)
			if err != nil {
				return err
			}
			var users []User
			if err := h.DB.SelectContext(ctx, &users, h.DB.Rebind(statement), args...); err != nil {
				return err
			}
			for _, user := range users {
				if err := user.setImageKey(); err != nil {
					return err
				}
				if user.AccountType != "certified_public" {
					user.Bio = ""
				}
				items = append(items, newRelationshipUserResponse(user))
			}
		}
		hasMore := len(items) > 50
		if hasMore {
			items = items[:50]
		}
		return c.JSON(http.StatusOK, searchUsersResponse{Items: items, HasMore: hasMore})
	}
}

type getUserRequest struct {
	UserID string `param:"userId"`
}

type getUserResponse relationshipUserResponse

func getUserHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getUserRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		viewer := GetSession(c).UserID
		user, err := getUser(ctx, h.DB, input.UserID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		}
		if err != nil {
			return err
		}
		blocked, err := isBlocked(ctx, h.DB, viewer, user.ID)
		if err != nil {
			return err
		}
		if blocked {
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		}
		friends, err := getFriendIDs(ctx, h.DB, viewer)
		if err != nil {
			return err
		}
		if user.AccountType != "certified_public" && !slices.Contains(friends, user.ID) {
			user.Bio = ""
		}
		return c.JSON(http.StatusOK, getUserResponse(newRelationshipUserResponse(user)))
	}
}

func newRelationshipUserResponse(user User) relationshipUserResponse {
	return relationshipUserResponse{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, ProfileImageURL: newGetMeResponse(user).ProfileImageURL, Bio: user.Bio, AccountType: user.AccountType}
}
