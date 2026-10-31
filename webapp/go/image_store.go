package isureal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// 新しい画像は書き込んだ台のファイルに置き、共有 DB には配信元だけを記録する。
// 投稿のたびに地域をまたいで本体を送らず、WAL も増やさない。
const imageStoreDirectory = "/home/isucon/images"

func imageFilePath(id string) (string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("invalid image id: %w", err)
	}
	return filepath.Join(imageStoreDirectory, id), nil
}

func writeImageFile(id string, data []byte) error {
	path, err := imageFilePath(id)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func readImageFile(id string) ([]byte, error) {
	path, err := imageFilePath(id)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func resetImageFiles() error {
	if err := os.RemoveAll(imageStoreDirectory); err != nil {
		return err
	}
	return os.MkdirAll(imageStoreDirectory, 0o755)
}

// fetchImageData は DB に本体がない画像を配信元から取得する。
func (c *imageCache) fetchImageData(ctx context.Context, image *cachedImage) error {
	if image.Origin == nil || len(image.Data) != 0 {
		return nil
	}
	if *image.Origin == c.region {
		data, err := readImageFile(image.ID)
		if err != nil {
			return err
		}
		image.Data = data
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, regionPeers[*image.Origin]+"/_internal/images/"+image.ID, nil)
	if err != nil {
		return err
	}
	request.Header.Set(internalTokenHeader, c.internalToken)
	response, err := peerClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("peer %s image %s: status %d", *image.Origin, image.ID, response.StatusCode)
	}
	image.Data, err = io.ReadAll(response.Body)
	return err
}
