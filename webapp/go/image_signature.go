package isureal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const imageURLLifetime = 24 * time.Hour
const imageSignatureIterations = 1000

// 署名は1件で HMAC を1000回計算するので、同じ画像の URL が同じになるよう期限を
// この単位で切り捨て、結果を再利用する。
const imageURLExpiryStep = 10 * 60
const imageSignatureCacheLimit = 500_000

var imageSignatureCache = struct {
	sync.Mutex
	values map[string][]byte
}{values: map[string][]byte{}}

func imageSigningKey(secretKeyHash string) ([]byte, error) {
	key, err := hex.DecodeString(secretKeyHash)
	if err != nil || len(key) != sha256.Size {
		return nil, fmt.Errorf("invalid image signing key")
	}
	return key, nil
}

func imageSignature(key []byte, imageID string, exp int64) []byte {
	cacheKey := strconv.FormatInt(exp, 10) + ":" + imageID + ":" + string(key)
	imageSignatureCache.Lock()
	cached, ok := imageSignatureCache.values[cacheKey]
	imageSignatureCache.Unlock()
	if ok {
		return cached
	}
	signature := computeImageSignature(key, imageID, exp)
	imageSignatureCache.Lock()
	if len(imageSignatureCache.values) >= imageSignatureCacheLimit {
		clear(imageSignatureCache.values)
	}
	imageSignatureCache.values[cacheKey] = signature
	imageSignatureCache.Unlock()
	return signature
}

func computeImageSignature(key []byte, imageID string, exp int64) []byte {
	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	mac := hmac.New(sha256.New, key)
	for range imageSignatureIterations {
		mac.Reset()
		mac.Write(value)
		value = mac.Sum(value[:0])
	}
	return value
}

func signedImageURL(user User, imageID string) string {
	exp := time.Now().Add(imageURLLifetime).Unix()
	exp -= exp % imageURLExpiryStep
	query := url.Values{
		"exp": {strconv.FormatInt(exp, 10)},
		"sig": {base64.RawURLEncoding.EncodeToString(imageSignature(user.ImageKey, imageID, exp))},
	}
	return "/api/images/" + url.PathEscape(imageID) + "?" + query.Encode()
}
