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
// この単位で切り上げ、結果を再利用する。初期画像は期限ごとに事前計算できる。
// 2026-12-01T00:00:00Z を基準にする。
const imageURLExpiryStep = 30 * 24 * 60 * 60
const imageURLExpiryBase = 1796083200
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

func imageSignatureCacheKey(key []byte, imageID string, exp int64) string {
	return strconv.FormatInt(exp, 10) + ":" + imageID + ":" + string(key)
}

func imageSignatureCached(key []byte, imageID string, exp int64) bool {
	if lookupPrecomputedSignature(imageID, exp) != nil {
		return true
	}
	cacheKey := imageSignatureCacheKey(key, imageID, exp)
	imageSignatureCache.Lock()
	defer imageSignatureCache.Unlock()
	_, ok := imageSignatureCache.values[cacheKey]
	return ok
}

func storeImageSignature(key []byte, imageID string, exp int64, signature []byte) {
	cacheKey := imageSignatureCacheKey(key, imageID, exp)
	imageSignatureCache.Lock()
	defer imageSignatureCache.Unlock()
	if len(imageSignatureCache.values) >= imageSignatureCacheLimit {
		clear(imageSignatureCache.values)
	}
	imageSignatureCache.values[cacheKey] = signature
}

func imageSignature(key []byte, imageID string, exp int64) []byte {
	if signature := lookupPrecomputedSignature(imageID, exp); signature != nil {
		return signature
	}
	imageSignatureCache.Lock()
	cached, ok := imageSignatureCache.values[imageSignatureCacheKey(key, imageID, exp)]
	imageSignatureCache.Unlock()
	if ok {
		return cached
	}
	signature := computeImageSignature(key, imageID, exp)
	storeImageSignature(key, imageID, exp, signature)
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

func currentImageURLExpiry() int64 {
	minimum := time.Now().Add(imageURLLifetime).Unix()
	steps := (minimum - imageURLExpiryBase + imageURLExpiryStep - 1) / imageURLExpiryStep
	return imageURLExpiryBase + max(steps, 0)*imageURLExpiryStep
}

func signedImageURL(user User, imageID string) string {
	exp := currentImageURLExpiry()
	query := url.Values{
		"exp": {strconv.FormatInt(exp, 10)},
		"sig": {base64.RawURLEncoding.EncodeToString(imageSignature(user.ImageKey, imageID, exp))},
	}
	return "/api/images/" + url.PathEscape(imageID) + "?" + query.Encode()
}
