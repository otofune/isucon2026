package isureal

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// friendRequestResult は関係変更の関数の結果。ok 以外では ID などは空になる。
type friendRequestResult struct {
	Result      string     `db:"result"`
	ID          string     `db:"id"`
	Status      string     `db:"status"`
	UpdatedAt   *time.Time `db:"updated_at"`
	RequesterID string     `db:"requester_id"`
	RecipientID string     `db:"recipient_id"`
}

type createFriendRequestRequest struct {
	RecipientID string `json:"recipientId"`
}

type createFriendRequestResponse struct {
	RelationID string    `json:"relationId"`
	Status     string    `json:"status"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func createFriendRequestHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input createFriendRequestRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		if actor == input.RecipientID || input.RecipientID == "" {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}
		var request friendRequestResult
		if err := h.DB.GetContext(ctx, &request, `SELECT * FROM isu_create_friend_request($1, $2, $3, $4)`, actor, input.RecipientID, uuid.NewString(), uuid.NewString()); err != nil {
			return err
		}
		switch request.Result {
		case "ok":
		case "not_found":
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		default:
			return NewAPIError(http.StatusConflict, "relationship_conflict")
		}
		if request.Status == "accepted" {
			if err := h.relationsChanged(ctx, request.RequesterID, request.RecipientID); err != nil {
				return err
			}
		}
		return c.JSON(http.StatusCreated, createFriendRequestResponse{RelationID: request.ID, Status: request.Status, UpdatedAt: *request.UpdatedAt})
	}
}

type transitionFriendRequestRequest struct {
	RequestID string `param:"requestId" json:"-"`
	Action    string `param:"action" json:"-"`
}

type transitionFriendRequestResponse createFriendRequestResponse

func transitionFriendRequestHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input transitionFriendRequestRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		var status string
		switch input.Action {
		case "accept":
			status = "accepted"
		case "reject":
			status = "rejected"
		case "cancel":
			status = "cancelled"
		default:
			return NewAPIError(http.StatusForbidden, "relationship_action_forbidden")
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		var request friendRequestResult
		if err := h.DB.GetContext(ctx, &request, `SELECT * FROM isu_transition_friend_request($1, $2, $3, $4)`, input.RequestID, actor, status, uuid.NewString()); err != nil {
			return err
		}
		switch request.Result {
		case "ok":
		case "not_found":
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		case "conflict":
			return NewAPIError(http.StatusConflict, "relationship_conflict")
		default:
			return NewAPIError(http.StatusForbidden, "relationship_action_forbidden")
		}
		if request.Status == "accepted" {
			if err := h.relationsChanged(ctx, request.RequesterID, request.RecipientID); err != nil {
				return err
			}
		}
		return c.JSON(http.StatusOK, transitionFriendRequestResponse{RelationID: request.ID, Status: request.Status, UpdatedAt: *request.UpdatedAt})
	}
}
