package isureal

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

type getMemoriesRequest struct {
	Month          string     `query:"month"`
	BeforePostedAt *time.Time `query:"beforePostedAt"`
	BeforeID       string     `query:"beforeId"`
}

type memoryPostResponse struct {
	createPostResponse
	RealMojis []replaceRealMojiResponse `json:"realmojis"`
	Comments  []createCommentResponse   `json:"comments"`
}

type getMemoriesResponse struct {
	Items   []memoryPostResponse `json:"items"`
	HasMore bool                 `json:"hasMore"`
}

type getMemoriesByDateRequest struct {
	Date           string     `param:"date"`
	BeforePostedAt *time.Time `query:"beforePostedAt"`
	BeforeID       string     `query:"beforeId"`
}

type getMemoriesByDateResponse getMemoriesResponse

func getMemoriesHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getMemoriesRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		start, err := time.Parse("2006-01", input.Month)
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_memory_period")
		}
		response, err := listMemories(c.Request().Context(), h, GetSession(c).UserID, start, start.AddDate(0, 1, 0), input.BeforePostedAt, input.BeforeID)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, response)
	}
}

func getMemoriesByDateHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getMemoriesByDateRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		start, err := time.Parse(time.DateOnly, input.Date)
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_memory_period")
		}
		response, err := listMemories(c.Request().Context(), h, GetSession(c).UserID, start, start.AddDate(0, 0, 1), input.BeforePostedAt, input.BeforeID)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, getMemoriesByDateResponse(response))
	}
}

func listMemories(ctx context.Context, h *HandlerContext, userID string, start, end time.Time, beforeAt *time.Time, beforeID string) (getMemoriesResponse, error) {
	defer func(start time.Time) { observe("memories.total", time.Since(start)) }(time.Now())
	db := h.DB
	var response getMemoriesResponse
	if (beforeAt == nil) != (beforeID == "") {
		return response, NewAPIError(http.StatusBadRequest, "invalid_pagination")
	}
	posts, err := selectPostsWithAuthor(ctx, db, `
  AND p.author_id = $1 AND w.local_date >= $2 AND w.local_date < $3
  AND ($4::timestamptz IS NULL OR (p.posted_at, p.id) < ($4, $5))
  ORDER BY p.posted_at DESC, p.id DESC LIMIT 51
 `, userID, start, end, beforeAt, beforeID)
	if err != nil {
		return response, err
	}
	if len(posts) > 50 {
		response.HasMore = true
		posts = posts[:50]
	}
	interactions, err := loadMemoryInteractions(ctx, db, posts)
	if err != nil {
		return response, err
	}
	signStart := time.Now()
	batch := newSignatureBatch()
	for _, post := range posts {
		batch.add(post.Author, post.ImageID)
		batch.addUser(post.Author)
	}
	for _, reaction := range interactions.reactions {
		batch.add(reaction.Reactor, reaction.ImageID)
		batch.addUser(reaction.Reactor)
	}
	for _, comment := range interactions.comments {
		batch.addUser(comment.Author)
	}
	batch.offload(ctx, h)
	items := make([]memoryPostResponse, 0, len(posts))
	for _, post := range posts {
		items = append(items, memoryPostResponse{
			createPostResponse: newPostResponse(post.Post, post.Author),
			RealMojis:          make([]replaceRealMojiResponse, 0),
			Comments:           make([]createCommentResponse, 0),
		})
	}
	attachMemoryInteractions(items, interactions)
	observe("memories.sign", time.Since(signStart))
	response.Items = items
	return response, nil
}

type memoryInteractions struct {
	reactions []RealMojiWithReactor
	comments  []CommentWithAuthor
}

func loadMemoryInteractions(ctx context.Context, db sqlx.QueryerContext, posts []PostWithAuthor) (memoryInteractions, error) {
	var interactions memoryInteractions
	if len(posts) == 0 {
		return interactions, nil
	}
	postIDs := make([]string, len(posts))
	for i := range posts {
		postIDs[i] = posts[i].ID
	}

	// 海外では SQL が1往復かかるので、2つの一覧を並行して読む。
	var reactions []RealMojiWithReactor
	var comments []CommentWithAuthor
	var reactionsErr, commentsErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		query, args, err := sqlx.In(`AND r.post_id IN (?) ORDER BY r.created_at DESC, r.id DESC`, postIDs)
		if err != nil {
			reactionsErr = err
			return
		}
		reactions, reactionsErr = selectCurrentRealMojis(ctx, db, sqlx.Rebind(sqlx.DOLLAR, query), args...)
	}()
	go func() {
		defer wg.Done()
		query, args, err := sqlx.In(`AND c.post_id IN (?) ORDER BY c.created_at, c.id`, postIDs)
		if err != nil {
			commentsErr = err
			return
		}
		comments, commentsErr = selectCommentsWithAuthor(ctx, db, sqlx.Rebind(sqlx.DOLLAR, query), args...)
	}()
	wg.Wait()
	if reactionsErr != nil {
		return interactions, reactionsErr
	}
	if commentsErr != nil {
		return interactions, commentsErr
	}
	// 1投稿につき50件までしか返さないので、署名の対象も先に絞る。
	reactionCounts := map[string]int{}
	for _, reaction := range reactions {
		if reactionCounts[reaction.PostID] < 50 {
			reactionCounts[reaction.PostID]++
			interactions.reactions = append(interactions.reactions, reaction)
		}
	}
	commentCounts := map[string]int{}
	for _, comment := range comments {
		if commentCounts[comment.PostID] < 50 {
			commentCounts[comment.PostID]++
			interactions.comments = append(interactions.comments, comment)
		}
	}
	return interactions, nil
}

func attachMemoryInteractions(items []memoryPostResponse, interactions memoryInteractions) {
	itemByPostID := make(map[string]*memoryPostResponse, len(items))
	for i := range items {
		itemByPostID[items[i].ID] = &items[i]
	}
	for _, reaction := range interactions.reactions {
		item := itemByPostID[reaction.PostID]
		item.RealMojis = append(item.RealMojis, newRealMojiResponse(reaction.RealMoji, reaction.Reactor))
	}
	for _, comment := range interactions.comments {
		item := itemByPostID[comment.PostID]
		item.Comments = append(item.Comments, newCommentResponse(comment.Comment, comment.Author))
	}
}
