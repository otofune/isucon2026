//go:build linux && cgo

package isureal

/*
#cgo LDFLAGS: -l:libturbojpeg.so.0
typedef void *tjhandle;
extern tjhandle tjInitDecompress(void);
extern int tjDecompressHeader3(tjhandle handle, const unsigned char *jpegBuf, unsigned long jpegSize, int *width, int *height, int *jpegSubsamp, int *jpegColorspace);
extern int tjDecompress2(tjhandle handle, const unsigned char *jpegBuf, unsigned long jpegSize, unsigned char *dstBuf, int width, int pitch, int height, int pixelFormat, int flags);
extern int tjDestroy(tjhandle handle);
*/
import "C"

import (
	"errors"
	"unsafe"
)

const (
	tjPixelRGB          = 0
	tjPixelCMYK         = 11
	tjColorspaceCMYK    = 3
	tjColorspaceYCCK    = 4
	tjFlagStopOnWarning = 8192
)

// cgo の呼び出しは GOMAXPROCS に数えられない。投稿が集中すると多数のスレッドが CPU を
// 奪い合い、DB 接続を持つ goroutine まで止まるので、大きな画像の同時実行を制限する。
// 1件にすると投稿の行列が長くなりすぎる。小さな画像はすぐ終わるので行列に並ばせない。
var jpegValidationSlot = make(chan struct{}, 2)

// validateJPEG は破損を含めて JPEG 全体を検証し、寸法を返す。1/8 に縮小して展開すると、
// 圧縮データはすべて復号して検査しつつ、逆 DCT と出力のコストを大きく減らせる。
func validateJPEG(data []byte) (width, height int, err error) {
	if len(data) == 0 {
		return 0, 0, errInvalidJPEG
	}
	if len(data) > jpegSlotThreshold {
		jpegValidationSlot <- struct{}{}
		defer func() { <-jpegValidationSlot }()
	}
	handle := C.tjInitDecompress()
	if handle == nil {
		return 0, 0, errors.New("failed to initialize turbojpeg")
	}
	defer C.tjDestroy(handle)
	input := (*C.uchar)(unsafe.SliceData(data))
	size := C.ulong(len(data))
	var cWidth, cHeight, subsampling, colorspace C.int
	if C.tjDecompressHeader3(handle, input, size, &cWidth, &cHeight, &subsampling, &colorspace) != 0 {
		return 0, 0, errInvalidJPEG
	}
	width, height = int(cWidth), int(cHeight)
	if width <= 0 || height <= 0 {
		return 0, 0, errInvalidJPEG
	}
	// 上限を超える画像は呼び出し側で拒否するので、展開しない。
	if width > 4096 || height > 4096 {
		return width, height, nil
	}
	pixelFormat, pixelSize := tjPixelRGB, 3
	if colorspace == tjColorspaceCMYK || colorspace == tjColorspaceYCCK {
		pixelFormat, pixelSize = tjPixelCMYK, 4
	}
	scaledWidth, scaledHeight := (width+7)/8, (height+7)/8
	pixels := make([]byte, scaledWidth*scaledHeight*pixelSize)
	if C.tjDecompress2(handle, input, size, (*C.uchar)(unsafe.SliceData(pixels)), C.int(scaledWidth), 0, C.int(scaledHeight), C.int(pixelFormat), tjFlagStopOnWarning) != 0 {
		return 0, 0, errInvalidJPEG
	}
	return width, height, nil
}
