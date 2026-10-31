//go:build !(linux && cgo)

package isureal

import (
	"bytes"
	"image/jpeg"
)

func validateJPEG(data []byte) (width, height int, err error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	return img.Bounds().Dx(), img.Bounds().Dy(), nil
}
