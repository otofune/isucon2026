package isureal

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"

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

// directoryCache は検索用の一覧を持つ。版は内容のハッシュを毎回 DB で計算するので、
// 書き込みの直後でも古い一覧は使わない。
type directoryCache struct {
	mu      sync.Mutex
	current *userDirectory
}

func (d *directoryCache) load(ctx context.Context, db *sqlx.DB) (*userDirectory, error) {
	var version string
	if err := db.GetContext(ctx, &version, `SELECT count(*)::text || ':' || COALESCE(sum(hashtextextended(id || ':' || username || ':' || display_name, 0)::numeric), 0)::text FROM users WHERE status='active'`); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.current != nil && d.current.version == version {
		return d.current, nil
	}
	var entries []directoryEntry
	// 順序は DB の照合順序に従う。版は一覧を読む前の値なので、読む間の更新は次回に反映される。
	if err := db.SelectContext(ctx, &entries, `SELECT id, username, display_name FROM users WHERE status='active' ORDER BY username, id`); err != nil {
		return nil, err
	}
	positions := make(map[string]int, len(entries))
	for i := range entries {
		entries[i].searchUsername = normalizeSearchText(entries[i].Username)
		entries[i].searchDisplayName = normalizeSearchText(entries[i].DisplayName)
		positions[entries[i].Username+"\x00"+entries[i].ID] = i
	}
	d.current = &userDirectory{version: version, entries: entries, positions: positions}
	return d.current, nil
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
