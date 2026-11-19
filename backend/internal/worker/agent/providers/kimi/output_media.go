package kimi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
)

var kimiMediaID = regexp.MustCompile(`^f_[0-9a-f]{64}$`)

type kimiMediaBudget struct {
	mu        sync.Mutex
	remaining int
}

func (budget *kimiMediaBudget) charge(previous, next int) bool {
	increment := base64.StdEncoding.EncodedLen(next) - base64.StdEncoding.EncodedLen(previous)
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if increment > budget.remaining {
		return false
	}
	budget.remaining -= increment
	return true
}

func (c *kimiClient) readMediaFiles(ctx context.Context, sessionID string, ids []string, maximum int, permits chan struct{}) (map[string]contracts.KimiConversationMediaFile, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	budget := &kimiMediaBudget{remaining: maximum}
	files := make([]contracts.KimiConversationMediaFile, len(ids))
	var next atomic.Int64
	var workers sync.WaitGroup
	var firstError error
	var recordError sync.Once
	for range min(kimiMediaConcurrency, len(ids)) {
		workers.Go(func() {
			for {
				index := int(next.Add(1) - 1)
				if index >= len(ids) {
					return
				}
				select {
				case permits <- struct{}{}:
				case <-ctx.Done():
					return
				}
				file, err := c.readMediaFile(ctx, sessionID, ids[index], budget)
				<-permits
				if err != nil {
					recordError.Do(func() { firstError = err; cancel() })
					return
				}
				files[index] = file
			}
		})
	}
	workers.Wait()
	if firstError != nil {
		return nil, firstError
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	byID := make(map[string]contracts.KimiConversationMediaFile, len(files))
	for index, file := range files {
		byID[ids[index]] = file
	}
	return byID, nil
}

func (c *kimiClient) readMediaFile(ctx context.Context, sessionID, id string, budget *kimiMediaBudget) (contracts.KimiConversationMediaFile, error) {
	response, err := c.endpoint.OpenStream(ctx, http.MethodGet, kimiSessionPath(sessionID, "/media/"+id), nil)
	if err != nil {
		return contracts.KimiConversationMediaFile{}, err
	}
	defer func() { _ = response.Body.Close() }()
	mimeType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || !kimiIsImageMIMEType(mimeType) {
		return contracts.KimiConversationMediaFile{}, fmt.Errorf("the Kimi media response has an unsupported image MIME type")
	}
	var data bytes.Buffer
	chunk := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return contracts.KimiConversationMediaFile{}, err
		}
		count, readErr := response.Body.Read(chunk)
		if count > 0 {
			if !budget.charge(data.Len(), data.Len()+count) {
				return contracts.KimiConversationMediaFile{}, fmt.Errorf("the Kimi image exceeds the remaining message size limit")
			}
			_, _ = data.Write(chunk[:count])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return contracts.KimiConversationMediaFile{}, readErr
		}
	}
	if data.Len() == 0 {
		return contracts.KimiConversationMediaFile{}, fmt.Errorf("the Kimi image has no bytes")
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(mimeType + "\x00"))
	_, _ = digest.Write(data.Bytes())
	if id != "f_"+hex.EncodeToString(digest.Sum(nil)) {
		return contracts.KimiConversationMediaFile{}, fmt.Errorf("the Kimi image bytes do not match their native digest")
	}
	return contracts.KimiConversationMediaFile{MIMEType: mimeType, Data: base64.StdEncoding.EncodeToString(data.Bytes())}, nil
}

func kimiIsImageMIMEType(value string) bool {
	return strings.HasPrefix(value, "image/") && len(value) > len("image/")
}
