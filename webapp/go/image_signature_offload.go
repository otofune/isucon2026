package isureal

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

// これ未満の未計算件数なら往復より手元で計算するほうが速い。
const signatureOffloadMinimum = 24
const signatureWorkerConcurrency = 2

type signatureItem struct {
	Key     string `json:"key"`
	ImageID string `json:"imageId"`
	Exp     int64  `json:"exp"`
}

type signatureRequest struct {
	Items []signatureItem `json:"items"`
}

type signatureResponse struct {
	Signatures [][]byte `json:"signatures"`
}

var signatureOffloadPeer atomic.Uint64

func (h *HandlerContext) signatureWorkers() []string {
	if h.Config.Region == "asia" {
		return []string{"europe", "america"}
	}
	return nil
}

// signatureBatch は応答を組み立てる前に署名をまとめて用意する。
type signatureBatch struct {
	exp   int64
	seen  map[string]bool
	items []signatureItem
	keys  [][]byte
}

func newSignatureBatch() *signatureBatch {
	return &signatureBatch{exp: currentImageURLExpiry(), seen: map[string]bool{}}
}

func (b *signatureBatch) add(user User, imageID string) {
	if b.seen[imageID] {
		return
	}
	b.seen[imageID] = true
	if imageSignatureCached(user.ImageKey, imageID, b.exp) {
		return
	}
	b.items = append(b.items, signatureItem{Key: hex.EncodeToString(user.ImageKey), ImageID: imageID, Exp: b.exp})
	b.keys = append(b.keys, user.ImageKey)
}

func (b *signatureBatch) addUser(user User) {
	if user.ProfileImageID != nil {
		b.add(user, *user.ProfileImageID)
	}
}

// offload はピアに計算を依頼する。失敗しても手元で計算するので、エラーは返さない。
func (b *signatureBatch) offload(ctx context.Context, h *HandlerContext) {
	workers := h.signatureWorkers()
	if len(workers) == 0 || len(b.items) < signatureOffloadMinimum {
		return
	}
	start := int(signatureOffloadPeer.Add(1))
	size := (len(b.items) + len(workers) - 1) / len(workers)
	var wg sync.WaitGroup
	for i := range workers {
		first := min(i*size, len(b.items))
		last := min(first+size, len(b.items))
		if first == last {
			continue
		}
		region := workers[(start+i)%len(workers)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			signatures, err := h.requestSignatures(ctx, region, b.items[first:last])
			if err != nil || len(signatures) != last-first {
				return
			}
			for j, signature := range signatures {
				storeImageSignature(b.keys[first+j], b.items[first+j].ImageID, b.exp, signature)
			}
		}()
	}
	wg.Wait()
}

func (h *HandlerContext) requestSignatures(ctx context.Context, region string, items []signatureItem) ([][]byte, error) {
	payload, err := json.Marshal(signatureRequest{Items: items})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, regionPeers[region]+"/_internal/sign", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(internalTokenHeader, h.Config.DBPassword)
	response, err := peerClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peer %s sign: status %d", region, response.StatusCode)
	}
	var decoded signatureResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded.Signatures, nil
}
