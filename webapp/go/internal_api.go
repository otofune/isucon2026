package isureal

import (
	"crypto/subtle"
	"net/http"

	"github.com/labstack/echo/v5"
)

const internalTokenHeader = "X-Isureal-Internal-Token"

type internalInvalidateRequest struct {
	UserID string `json:"userId"`
}

func internalOnly(h *HandlerContext) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			token := c.Request().Header.Get(internalTokenHeader)
			if subtle.ConstantTimeCompare([]byte(token), []byte(h.Config.DBPassword)) != 1 {
				return echo.ErrNotFound
			}
			return next(c)
		}
	}
}

func internalInvalidateSessionHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input internalInvalidateRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		h.Sessions.invalidateUser(input.UserID)
		return c.NoContent(http.StatusNoContent)
	}
}

func internalResetHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		h.Sessions.clear()
		h.Images.clear()
		return c.NoContent(http.StatusNoContent)
	}
}
