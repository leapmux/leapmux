package kimi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

var kimiMediaID = regexp.MustCompile(`^f_[0-9a-f]{64}$`)

type kimiTranscriptMedia struct {
	ids      []string
	identity kimiTranscriptToolIdentity
	maximum  int
}

// parseTranscriptMedia validates references before admitting any network work.
func parseTranscriptMedia(sessionID, agentID string, op kimiTranscriptOperation, original []byte) (*kimiTranscriptMedia, error) {
	var frame kimiTranscriptToolFrame
	if json.Unmarshal(op.Frame, &frame) != nil {
		return nil, nil
	}
	var parts []struct {
		Type   string `json:"type"`
		Source struct {
			Kind   string `json:"kind"`
			FileID string `json:"file_id"`
		} `json:"source"`
	}
	if json.Unmarshal(frame.Output, &parts) != nil {
		return nil, nil
	}
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, part := range parts {
		if part.Type != contracts.KimiOutputPartImage || part.Source.Kind != contracts.KimiMediaSourceSessionMedia {
			continue
		}
		if !kimiMediaID.MatchString(part.Source.FileID) {
			return nil, fmt.Errorf("the Kimi image has an invalid media ID")
		}
		if _, exists := seen[part.Source.FileID]; !exists {
			seen[part.Source.FileID] = struct{}{}
			ids = append(ids, part.Source.FileID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := kimiCheckID("session", sessionID); err != nil {
		return nil, err
	}
	if agentID == "" || op.TurnID == "" || op.StepID == "" || frame.FrameID == "" || frame.ToolCallID == "" {
		return nil, fmt.Errorf("the Kimi image has no complete tool identity")
	}
	maximum := agent.LiveMaxMessageSize() - len(original) - 1024
	if maximum <= 0 {
		return nil, fmt.Errorf("the Kimi image exceeds the message size limit")
	}
	return &kimiTranscriptMedia{ids: ids, maximum: maximum, identity: kimiTranscriptToolIdentity{sessionID: sessionID, agentID: agentID, turnID: op.TurnID, stepID: op.StepID, frameID: frame.FrameID, toolCallID: frame.ToolCallID}}, nil
}

// recoverTranscriptMedia is the synchronous downloader used by direct media checks.
// Native event handling admits the same work to the tracked media service.
func (a *Agent) recoverTranscriptMedia(sessionID, agentID string, op kimiTranscriptOperation, original []byte) ([]byte, error) {
	media, err := parseTranscriptMedia(sessionID, agentID, op, original)
	if err != nil || media == nil {
		return nil, err
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	return a.readTranscriptMedia(ctx, media, make(chan struct{}, kimiMediaConcurrency))
}

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

func (a *Agent) readTranscriptMedia(ctx context.Context, media *kimiTranscriptMedia, permits chan struct{}) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	budget := &kimiMediaBudget{remaining: media.maximum}
	files := make([]map[string]string, len(media.ids))
	var next atomic.Int64
	var workers sync.WaitGroup
	var firstError error
	var recordError sync.Once
	for range min(kimiMediaConcurrency, len(media.ids)) {
		workers.Go(func() {
			for {
				index := int(next.Add(1) - 1)
				if index >= len(media.ids) {
					return
				}
				select {
				case permits <- struct{}{}:
				case <-ctx.Done():
					return
				}
				file, err := a.readTranscriptMediaFile(ctx, media.identity.sessionID, media.ids[index], budget)
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
	byID := make(map[string]any, len(files))
	for index, file := range files {
		byID[media.ids[index]] = file
	}
	return media.identity.supplement(contracts.KimiTranscriptSupplementMedia, map[string]any{contracts.KimiTranscriptSupplementFiles: byID}, media.maximum)
}

func (a *Agent) readTranscriptMediaFile(ctx context.Context, sessionID, id string, budget *kimiMediaBudget) (map[string]string, error) {
	response, err := a.api.endpoint.OpenStream(ctx, http.MethodGet, kimiSessionPath(sessionID, "/media/"+id), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	mimeType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || !kimiIsImageMIMEType(mimeType) {
		return nil, fmt.Errorf("the Kimi media response has an unsupported image MIME type")
	}
	var data bytes.Buffer
	chunk := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, readErr := response.Body.Read(chunk)
		if count > 0 {
			if !budget.charge(data.Len(), data.Len()+count) {
				return nil, fmt.Errorf("the Kimi image exceeds the remaining message size limit")
			}
			_, _ = data.Write(chunk[:count])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	if data.Len() == 0 {
		return nil, fmt.Errorf("the Kimi image has no bytes")
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(mimeType + "\x00"))
	_, _ = digest.Write(data.Bytes())
	if id != "f_"+hex.EncodeToString(digest.Sum(nil)) {
		return nil, fmt.Errorf("the Kimi image bytes do not match their native digest")
	}
	return map[string]string{contracts.KimiTranscriptSupplementMIMEType: mimeType, contracts.KimiTranscriptSupplementData: base64.StdEncoding.EncodeToString(data.Bytes())}, nil
}

func kimiIsImageMIMEType(value string) bool {
	return strings.HasPrefix(value, "image/") && len(value) > len("image/")
}
