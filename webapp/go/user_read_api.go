package isureal

import (
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

const searchTextCacheLimit = 200_000

var searchTextCache = struct {
	sync.RWMutex
	values map[string]string
}{values: map[string]string{}}

func cachedSearchText(value string) string {
	searchTextCache.RLock()
	normalized, ok := searchTextCache.values[value]
	searchTextCache.RUnlock()
	if ok {
		return normalized
	}
	normalized = normalizeSearchText(value)
	searchTextCache.Lock()
	if len(searchTextCache.values) >= searchTextCacheLimit {
		clear(searchTextCache.values)
	}
	searchTextCache.values[value] = normalized
	searchTextCache.Unlock()
	return normalized
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
		// 判定に必要な列だけ全件取得し、該当した利用者だけ詳細を読む。
		var candidates []struct {
			ID          string `db:"id"`
			Username    string `db:"username"`
			DisplayName string `db:"display_name"`
		}
		if err := h.DB.SelectContext(ctx, &candidates, `SELECT id, username, display_name FROM users WHERE status='active' AND id<>$1 AND ($2='' OR (username,id)>($2,$3)) ORDER BY username, id`, viewer, input.BeforeUsername, input.BeforeID); err != nil {
			return err
		}
		blocked, err := getBlockedUserIDs(ctx, h.DB, viewer)
		if err != nil {
			return err
		}
		query := normalizeSearchText(input.Q)
		matchedIDs := make([]string, 0, 51)
		for _, user := range candidates {
			if !strings.Contains(cachedSearchText(user.Username), query) && !strings.Contains(cachedSearchText(user.DisplayName), query) {
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
