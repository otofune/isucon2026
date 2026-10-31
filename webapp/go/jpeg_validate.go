package isureal

import (
	"bytes"
	"errors"
	"image/jpeg"
)

// 投稿画像の完全なデコードは1枚で数十msかかり、期間開始時に CPU を使い切る。
// 大きな入力はヘッダーと構造だけを確認し、短い入力は従来どおり完全に検証する。
const jpegFullDecodeLimit = 128 * 1024

var (
	jpegStartOfScan = []byte{0xFF, 0xDA}
	jpegEndOfImage  = []byte{0xFF, 0xD9}
)

func validateJPEG(data []byte) (width, height int, err error) {
	if len(data) <= jpegFullDecodeLimit {
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return 0, 0, err
		}
		return img.Bounds().Dx(), img.Bounds().Dy(), nil
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	scan := bytes.LastIndex(data, jpegStartOfScan)
	if scan < 0 || bytes.LastIndex(data, jpegEndOfImage) < scan {
		return 0, 0, errors.New("truncated jpeg")
	}
	return config.Width, config.Height, nil
}
