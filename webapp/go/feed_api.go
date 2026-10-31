package isureal

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

type getFeedRequest struct {
	Feed           string     `param:"feed"`
	BeforeScore    *int64     `query:"beforeScore"`
	BeforePostedAt *time.Time `query:"beforePostedAt"`
	BeforeID       string     `query:"beforeId"`
}

type feedItemResponse struct {
	createPostResponse
	Score *int64 `json:"score,omitempty"`
}

type getFeedResponse struct {
	Items   []feedItemResponse `json:"items"`
	HasMore bool               `json:"hasMore"`
}

func getFeedHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getFeedRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		switch input.Feed {
		case "friends", "friends-of-friends", "public", "following":
		default:
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if (input.BeforePostedAt == nil) != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		ranked := input.Feed == "friends-of-friends"
		if ranked && (input.BeforeScore == nil) != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		defer func(start time.Time) { observe("feed.total", time.Since(start)) }(time.Now())
		ctx := c.Request().Context()
		// 友達と公開のフィードの判定に友達の友達は使わない。
		load := loadViewerContext
		if input.Feed == "friends" || input.Feed == "public" {
			load = loadDirectViewerContext
		}
		viewer, err := load(ctx, h.DB, GetSession(c).UserID)
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusOK, getFeedResponse{Items: []feedItemResponse{}})
		}
		if err != nil {
			return err
		}
		// 当日の投稿がなければ他人の投稿は1件も見えない。
		if !viewer.HasPostedToday {
			return c.JSON(http.StatusOK, getFeedResponse{Items: []feedItemResponse{}})
		}
		following := map[string]bool{}
		if input.Feed == "following" {
			var followeeIDs []string
			if err := h.DB.SelectContext(ctx, &followeeIDs, `SELECT followee_id FROM follows WHERE follower_id = $1`, viewer.ID); err != nil {
				return err
			}
			for _, id := range followeeIDs {
				following[id] = true
			}
		}
		beforePostedAt, beforeID := input.BeforePostedAt, input.BeforeID
		if ranked {
			beforePostedAt, beforeID = nil, ""
		}
		posts, err := selectFeedCandidates(ctx, h.DB, viewer, input.Feed, following, beforePostedAt, beforeID)
		if err != nil {
			return err
		}
		candidates := make([]PostWithAuthor, 0)
		for _, post := range posts {
			if viewer.canView(post.Post) && matchesFeed(viewer, following, post.Post, input.Feed) {
				candidates = append(candidates, post)
			}
		}
		var scores map[string]int64
		if ranked {
			if scores, err = recommendationScores(ctx, h.DB, viewer, candidates); err != nil {
				return err
			}
			sort.SliceStable(candidates, func(i, j int) bool {
				return scores[candidates[i].ID] > scores[candidates[j].ID]
			})
		}
		// 返す範囲の先頭だけを先に署名する。
		signStart := time.Now()
		batch := newSignatureBatch()
		for _, post := range candidates[:min(len(candidates), 51)] {
			batch.add(post.Author, post.ImageID)
			batch.addUser(post.Author)
		}
		batch.offload(ctx, h)
		items := make([]feedItemResponse, 0)
		for _, post := range candidates {
			score := scores[post.ID]
			if ranked && input.BeforeScore != nil && !rankedBefore(score, post.Post, *input.BeforeScore, *input.BeforePostedAt, input.BeforeID) {
				continue
			}
			item := feedItemResponse{createPostResponse: newPostResponse(post.Post, post.Author)}
			if ranked {
				item.Score = &score
			}
			items = append(items, item)
			if len(items) == 51 {
				break
			}
		}
		observe("feed.sign", time.Since(signStart))
		hasMore := len(items) > 50
		if hasMore {
			items = items[:50]
		}
		return c.JSON(http.StatusOK, getFeedResponse{Items: items, HasMore: hasMore})
	}
}

// selectFeedCandidates はフィードに表示する投稿を SQL の段階で絞る。ランキング以外は
// viewerContext.canView と matchesFeed の判定を SQL に写しているので、上限を掛けられる。
// 判定そのものは呼び出し側に残し、この関数は候補を減らすだけにする。
func selectFeedCandidates(ctx context.Context, db sqlx.QueryerContext, viewer viewerContext, feed string, following map[string]bool, beforePostedAt *time.Time, beforeID string) ([]PostWithAuthor, error) {
	const common = `
   AND p.author_id <> $1 AND w.ended_at IS NULL
   AND ($2::timestamptz IS NULL OR (p.posted_at, p.id) < ($2, $3))
   AND NOT p.author_id = ANY($4::varchar[])
  `
	const order = ` ORDER BY p.posted_at DESC, p.id DESC`
	const page = ` LIMIT 51`
	blocked := mapKeys(viewer.Blocked)
	switch feed {
	case "friends":
		return selectPostsWithAuthor(ctx, db, common+` AND p.author_id = ANY($5::varchar[])`+order+page,
			viewer.ID, beforePostedAt, beforeID, blocked, mapKeys(viewer.Friends))
	case "public":
		return selectPostsWithAuthor(ctx, db, common+` AND p.visibility = 'public'`+order+page,
			viewer.ID, beforePostedAt, beforeID, blocked)
	case "following":
		return selectPostsWithAuthor(ctx, db, common+`
   AND p.author_id = ANY($5::varchar[])
   AND (p.visibility = 'public' OR p.author_id = ANY($6::varchar[])
        OR (p.visibility = 'friends_of_friends' AND p.author_id = ANY($7::varchar[])))
  `+order+page, viewer.ID, beforePostedAt, beforeID, blocked, mapKeys(following), mapKeys(viewer.Friends), mapKeys(viewer.FriendsOfFriends))
	default:
		return selectPostsWithAuthor(ctx, db, common+`
   AND p.author_id = ANY($5::varchar[]) AND p.visibility IN ('public', 'friends_of_friends')
  `+order, viewer.ID, beforePostedAt, beforeID, blocked, mapKeys(viewer.FriendsOfFriends))
	}
}

func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func rankedBefore(score int64, post Post, beforeScore int64, beforePostedAt time.Time, beforeID string) bool {
	if score != beforeScore {
		return score < beforeScore
	}
	if !post.PostedAt.Equal(beforePostedAt) {
		return post.PostedAt.Before(beforePostedAt)
	}
	return post.ID < beforeID
}

func recommendationScores(ctx context.Context, db sqlx.QueryerContext, viewer viewerContext, posts []PostWithAuthor) (map[string]int64, error) {
	scores := make(map[string]int64, len(posts))
	if len(posts) == 0 {
		return scores, nil
	}
	authorIDs := make([]string, 0, len(posts))
	postIDs := make([]string, 0, len(posts))
	for _, post := range posts {
		authorIDs = append(authorIDs, post.AuthorID)
		postIDs = append(postIDs, post.ID)
	}

	query, args, err := sqlx.In(`SELECT user_low_id, user_high_id FROM friendships WHERE user_low_id IN (?) OR user_high_id IN (?)`, authorIDs, authorIDs)
	if err != nil {
		return nil, err
	}
	var friendships []struct {
		UserLowID  string `db:"user_low_id"`
		UserHighID string `db:"user_high_id"`
	}
	if err := sqlx.SelectContext(ctx, db, &friendships, sqlx.Rebind(sqlx.DOLLAR, query), args...); err != nil {
		return nil, err
	}
	connections := map[string]int64{}
	for _, friendship := range friendships {
		connections[friendship.UserLowID] += int64(viewer.MutualFriends[friendship.UserHighID])
		connections[friendship.UserHighID] += int64(viewer.MutualFriends[friendship.UserLowID])
	}

	query, args, err = sqlx.In(`
  SELECT r.post_id, r.reactor_id AS user_id FROM realmojis r
  JOIN users u ON u.id = r.reactor_id AND u.status = 'active'
  WHERE r.post_id IN (?) AND r.replaced_at IS NULL AND r.deleted_at IS NULL
  UNION
  SELECT c.post_id, c.author_id AS user_id FROM comments c
  JOIN users u ON u.id = c.author_id AND u.status = 'active'
  WHERE c.post_id IN (?) AND c.deleted_at IS NULL
 `, postIDs, postIDs)
	if err != nil {
		return nil, err
	}
	var engagements []struct {
		PostID string `db:"post_id"`
		UserID string `db:"user_id"`
	}
	if err := sqlx.SelectContext(ctx, db, &engagements, sqlx.Rebind(sqlx.DOLLAR, query), args...); err != nil {
		return nil, err
	}
	friendEngagements := map[string]int64{}
	for _, engagement := range engagements {
		if viewer.Friends[engagement.UserID] {
			friendEngagements[engagement.PostID]++
		}
	}

	for _, post := range posts {
		scores[post.ID] = 10*friendEngagements[post.ID] + 3*int64(viewer.MutualFriends[post.AuthorID]) + min(connections[post.AuthorID], 30)
	}
	return scores, nil
}

func matchesFeed(viewer viewerContext, following map[string]bool, post Post, feed string) bool {
	switch feed {
	case "public":
		return post.Visibility == "public"
	case "following":
		return following[post.AuthorID]
	case "friends":
		return viewer.Friends[post.AuthorID]
	case "friends-of-friends":
		return !viewer.Friends[post.AuthorID] && viewer.FriendsOfFriends[post.AuthorID]
	default:
		return false
	}
}
