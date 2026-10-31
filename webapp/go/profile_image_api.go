package isureal

import (
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

func putProfileImageHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if c.Request().Header.Get("Content-Type") != "image/jpeg" {
			return NewAPIError(http.StatusUnsupportedMediaType, "unsupported_media_type")
		}
		data, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return err
		}
		width, height, err := validateJPEG(data)
		if err != nil {
			c.Set("abuse", "invalid_image")
			return NewAPIError(http.StatusBadRequest, "invalid_profile_image")
		}
		if width > 512 || height > 512 {
			return NewAPIError(http.StatusBadRequest, "invalid_profile_image")
		}

		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		imageID := uuid.NewString()
		// 更新できなかったときは参照されないファイルが残るが、初期化で消える。
		if err := writeImageFile(imageID, data); err != nil {
			return err
		}
		var result string
		if err := h.DB.GetContext(ctx, &result, `SELECT isu_replace_profile_image($1, $2, $3)`, userID, imageID, h.Config.Region); err != nil {
			return err
		}
		if result != "ok" {
			return NewAPIError(http.StatusNotFound, "account_not_found")
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func deleteProfileImageHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		var result string
		if err := h.DB.GetContext(ctx, &result, `SELECT isu_delete_profile_image($1)`, userID); err != nil {
			return err
		}
		if result != "ok" {
			return NewAPIError(http.StatusNotFound, "account_not_found")
		}
		return c.NoContent(http.StatusNoContent)
	}
}
