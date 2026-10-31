package isureal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// asia の投稿画像の検証を、同じリージョンで余裕のある DB 台に任せる。
// 失敗したときと混んでいるときは手元で検証するので、DB 台が止まっても投稿はできる。
const jpegWorkerURL = "http://192.168.4.10:8080"

const jpegSlotThreshold = 128 * 1024

var jpegRemoteSlot = make(chan struct{}, 2)

var (
	errInvalidJPEG           = errors.New("invalid jpeg")
	errJPEGWorkerUnavailable = errors.New("jpeg worker unavailable")
)

type jpegSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (h *HandlerContext) validatePostJPEG(ctx context.Context, data []byte) (width, height int, err error) {
	if h.Config.Region == "asia" && len(data) > jpegSlotThreshold {
		select {
		case jpegRemoteSlot <- struct{}{}:
			width, height, err = h.validateRemoteJPEG(ctx, data)
			<-jpegRemoteSlot
			if !errors.Is(err, errJPEGWorkerUnavailable) {
				return width, height, err
			}
		default:
		}
	}
	return validateJPEG(data)
}

func (h *HandlerContext) validateRemoteJPEG(ctx context.Context, data []byte) (int, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, jpegWorkerURL+"/_internal/jpeg", bytes.NewReader(data))
	if err != nil {
		return 0, 0, errJPEGWorkerUnavailable
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set(internalTokenHeader, h.Config.DBPassword)
	response, err := peerClient.Do(request)
	if err != nil {
		return 0, 0, errJPEGWorkerUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		var size jpegSize
		if err := json.NewDecoder(response.Body).Decode(&size); err != nil {
			return 0, 0, errJPEGWorkerUnavailable
		}
		return size.Width, size.Height, nil
	case http.StatusUnprocessableEntity:
		return 0, 0, errInvalidJPEG
	default:
		return 0, 0, errJPEGWorkerUnavailable
	}
}
