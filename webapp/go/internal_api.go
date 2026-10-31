package isureal

import (
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync/atomic"

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

func internalWakeNotificationsHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		h.NotificationWake.wakeAll()
		return c.NoContent(http.StatusNoContent)
	}
}

func internalResetHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		h.Sessions.clear()
		h.Images.clear()
		if err := resetImageFiles(); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func internalGetImageHandler() echo.HandlerFunc {
	return func(c *echo.Context) error {
		data, err := readImageFile(c.Param("imageId"))
		if err != nil {
			return echo.ErrNotFound
		}
		return c.Blob(http.StatusOK, "application/octet-stream", data)
	}
}

func internalGetSignatureTableHandler() echo.HandlerFunc {
	return func(c *echo.Context) error {
		http.ServeFile(c.Response(), c.Request(), signatureTablePath(currentImageURLExpiry()))
		return nil
	}
}

func internalSignHandler() echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input signatureRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		signatures := make([][]byte, len(input.Items))
		var next atomic.Int64
		var failed atomic.Bool
		done := make(chan struct{}, signatureWorkerConcurrency)
		for range signatureWorkerConcurrency {
			go func() {
				defer func() { done <- struct{}{} }()
				for {
					i := int(next.Add(1)) - 1
					if i >= len(input.Items) {
						return
					}
					item := input.Items[i]
					key, err := hex.DecodeString(item.Key)
					if err != nil {
						failed.Store(true)
						return
					}
					signatures[i] = imageSignature(key, item.ImageID, item.Exp)
				}
			}()
		}
		for range signatureWorkerConcurrency {
			<-done
		}
		if failed.Load() {
			return echo.ErrBadRequest
		}
		return c.JSON(http.StatusOK, signatureResponse{Signatures: signatures})
	}
}
