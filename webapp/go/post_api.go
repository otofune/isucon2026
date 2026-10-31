package isureal

import (
	"database/sql"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type createPostRequest struct {
	Image        *multipart.FileHeader `form:"image"`
	Caption      string                `form:"caption"`
	Visibility   string                `form:"visibility"`
	LocationKind string                `form:"location_kind"`
	Latitude     *float64              `form:"latitude"`
	Longitude    *float64              `form:"longitude"`
	Locality     *string               `form:"locality"`
}

type postAuthorResponse struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"displayName"`
	ProfileImageURL *string `json:"profileImageUrl"`
	Bio             string  `json:"bio"`
	AccountType     string  `json:"accountType"`
}

type createPostResponse struct {
	ID              string             `json:"id"`
	Author          postAuthorResponse `json:"author"`
	PostingWindowID string             `json:"postingWindowId"`
	ImageURL        string             `json:"imageUrl"`
	Caption         string             `json:"caption"`
	Visibility      string             `json:"visibility"`
	Location        PostLocation       `json:"location"`
	PostedAt        time.Time          `json:"postedAt"`
	OnTime          bool               `json:"onTime"`
	LateSeconds     int64              `json:"lateSeconds"`
}

func createPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input createPostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if input.Image == nil || !validUserText(input.Caption, 0, 1000) {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		switch input.Visibility {
		case "friends", "friends_of_friends", "public":
		default:
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		location := PostLocation{
			Kind: input.LocationKind, Latitude: input.Latitude,
			Longitude: input.Longitude, Locality: input.Locality,
		}
		if !location.valid() {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		imageFile, err := input.Image.Open()
		if err != nil {
			return err
		}
		defer imageFile.Close()
		imageData, err := io.ReadAll(imageFile)
		if err != nil {
			return err
		}
		width, height, err := validateJPEG(imageData)
		if err != nil || width > 4096 || height > 4096 {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}

		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		imageID := uuid.NewString()
		postID := uuid.NewString()
		// 投稿できなかったときは参照されないファイルが残るが、初期化で消える。
		if err := writeImageFile(imageID, imageData); err != nil {
			return err
		}
		var created struct {
			Result string `db:"result"`
			User
			WindowID       string     `db:"window_id"`
			PostedAt       *time.Time `db:"posted_at"`
			OnTimeDeadline *time.Time `db:"on_time_deadline"`
		}
		if err := h.DB.GetContext(ctx, &created, `SELECT * FROM isu_create_post($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			userID, postID, imageID, input.Caption, input.Visibility,
			location.Kind, location.Latitude, location.Longitude, location.Locality, h.Config.Region); err != nil {
			return err
		}
		switch created.Result {
		case "ok":
		case "unavailable":
			return NewAPIError(http.StatusConflict, "post_slot_unavailable")
		default:
			return sql.ErrNoRows
		}
		user := created.User
		if err := user.setImageKey(); err != nil {
			return err
		}
		post := Post{
			ID: postID, AuthorID: userID, PostingWindowID: created.WindowID, ImageID: imageID,
			Caption: input.Caption, Visibility: input.Visibility, PostLocation: location,
			PostedAt: *created.PostedAt, OnTimeDeadline: *created.OnTimeDeadline,
		}
		return c.JSON(http.StatusCreated, newPostResponse(post, user))
	}
}

func newPostResponse(post Post, user User) createPostResponse {
	response := createPostResponse{
		ID: post.ID,
		Author: postAuthorResponse{
			ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
			ProfileImageURL: newGetMeResponse(user).ProfileImageURL,
			Bio:             user.Bio, AccountType: user.AccountType,
		},
		PostingWindowID: post.PostingWindowID, ImageURL: signedImageURL(user, post.ImageID),
		Caption: post.Caption, Visibility: post.Visibility, Location: post.PostLocation,
		PostedAt: post.PostedAt, OnTime: !post.PostedAt.After(post.OnTimeDeadline),
	}
	if !response.OnTime {
		response.LateSeconds = int64(post.PostedAt.Sub(post.OnTimeDeadline) / time.Second)
	}
	return response
}

type getMyCurrentPostResponse createPostResponse

func getMyCurrentPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		posts, err := selectPostsWithAuthor(ctx, h.DB, `
   AND p.author_id = $1
   AND EXISTS (
     SELECT 1 FROM posting_windows current
     WHERE current.region = u.region AND current.ended_at IS NULL AND current.local_date = w.local_date
   )
   LIMIT 1
  `, userID)
		if err != nil {
			return err
		}
		if len(posts) == 0 {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		return c.JSON(http.StatusOK, getMyCurrentPostResponse(newPostResponse(posts[0].Post, posts[0].Author)))
	}
}

type getPostRequest struct {
	PostID string `param:"postId"`
}

type getPostResponse createPostResponse

func getPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getPostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		post, err := getPostWithAuthor(ctx, h.DB, input.PostID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		viewer, err := loadViewerContext(ctx, h.DB, GetSession(c).UserID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err != nil || !viewer.canView(post.Post) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		return c.JSON(http.StatusOK, getPostResponse(newPostResponse(post.Post, post.Author)))
	}
}

type updatePostRequest struct {
	PostID     string  `param:"postId" json:"-"`
	Caption    *string `json:"caption"`
	Visibility *string `json:"visibility"`
}

type updatePostResponse createPostResponse

func updatePostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input updatePostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if input.Caption == nil && input.Visibility == nil {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if input.Caption != nil && !validUserText(*input.Caption, 0, 1000) {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if input.Visibility != nil {
			switch *input.Visibility {
			case "friends", "friends_of_friends", "public":
			default:
				return NewAPIError(http.StatusBadRequest, "invalid_post")
			}
		}
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		var result string
		if err := h.DB.GetContext(ctx, &result, `SELECT isu_update_post($1, $2, $3, $4)`, userID, input.PostID, input.Caption, input.Visibility); err != nil {
			return err
		}
		switch result {
		case "ok":
		case "not_found":
			return NewAPIError(http.StatusNotFound, "post_not_found")
		default:
			return NewAPIError(http.StatusForbidden, "post_not_editable")
		}
		post, err := getPostWithAuthor(ctx, h.DB, input.PostID)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, updatePostResponse(newPostResponse(post.Post, post.Author)))
	}
}

type deletePostRequest struct {
	PostID string `param:"postId" json:"-"`
}

func deletePostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input deletePostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		var result string
		if err := h.DB.GetContext(ctx, &result, `SELECT isu_delete_post($1, $2)`, userID, input.PostID); err != nil {
			return err
		}
		switch result {
		case "ok":
			return c.NoContent(http.StatusNoContent)
		case "not_found":
			return NewAPIError(http.StatusNotFound, "post_not_found")
		case "forbidden":
			return NewAPIError(http.StatusForbidden, "post_not_editable")
		default:
			return NewAPIError(http.StatusConflict, "post_slot_unavailable")
		}
	}
}
