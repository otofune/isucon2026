package isureal

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type openPostingWindowRequest struct {
	Region        string `json:"region"`
	LocalDate     string `json:"localDate"`
	OnTimeSeconds *int64 `json:"onTimeSeconds"`
}

type openPostingWindowResponse struct {
	ID             string    `json:"id"`
	Region         string    `json:"region"`
	LocalDate      string    `json:"localDate"`
	StartsAt       time.Time `json:"startsAt"`
	OnTimeDeadline time.Time `json:"onTimeDeadline"`
}

func openPostingWindowHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input openPostingWindowRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		region := input.Region
		localDate, err := time.Parse(time.DateOnly, input.LocalDate)
		if !validRegion(region) || err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_posting_window")
		}
		seconds := int64(24 * 60 * 60)
		if input.OnTimeSeconds != nil {
			seconds = *input.OnTimeSeconds
		}
		ctx := c.Request().Context()
		var opened struct {
			Result         string     `db:"result"`
			StartsAt       *time.Time `db:"starts_at"`
			OnTimeDeadline *time.Time `db:"on_time_deadline"`
		}
		windowID := uuid.NewString()
		if err := h.DB.GetContext(ctx, &opened, `SELECT * FROM isu_open_posting_window($1, $2, $3, $4)`, windowID, region, input.LocalDate, seconds); err != nil {
			return err
		}
		if opened.Result != "ok" {
			return NewAPIError(http.StatusConflict, "posting_window_conflict")
		}
		// 通知の正本は DB にあるので、合図に失敗しても SSE の定期確認で届く。
		if region == h.Config.Region {
			h.NotificationWake.wakeAll()
		} else if err := h.callPeer(ctx, region, "/_internal/notifications/wake", struct{}{}); err != nil {
			c.Logger().Error("failed to wake notifications", "error", err)
		}
		return c.JSON(http.StatusCreated, openPostingWindowResponse{
			ID: windowID, Region: region, LocalDate: localDate.Format(time.DateOnly),
			StartsAt: *opened.StartsAt, OnTimeDeadline: *opened.OnTimeDeadline,
		})
	}
}
