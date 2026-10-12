package deepseekharness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

type imagePosition struct {
	Position   *int                 `json:"position"`
	Attachment nativeImageReference `json:"attachment"`
}

type imageValue struct {
	Attachment nativeImageReference `json:"attachment"`
	Data       string               `json:"data"`
}

type imageReceipt struct {
	SessionID      string           `json:"sessionId"`
	CallID         string           `json:"callId"`
	ToolName       string           `json:"toolName"`
	IsError        *bool            `json:"isError"`
	OriginalImages []imagePosition  `json:"originalImages"`
	RetainedImages *[]imagePosition `json:"retainedImages"`
	Images         []imageValue     `json:"images"`
}

func imageReceiptLeaf(sessionID, callID string) string {
	session := sha256.Sum256([]byte(sessionID))
	call := sha256.Sum256([]byte(callID))
	return hex.EncodeToString(session[:]) + "." + hex.EncodeToString(call[:]) + ".json"
}

func readImageReceipt(ctx context.Context, directory, leaf string, maximum int) (data []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || maximum <= 0 {
		return nil, errors.New("the DeepSeek Harness image receipt requires a private directory and a positive size limit")
	}
	root, err := sessionstore.OpenArchiveRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			data = nil
			err = errors.Join(err, closeErr)
		}
	}()
	data, err = sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, int64(maximum), leaf)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, errors.New("the DeepSeek Harness image receipt is not valid UTF-8")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func nativeImagePositions(content json.RawMessage) ([]imagePosition, error) {
	refs, err := nativeImageReferences(content)
	if err != nil {
		return nil, err
	}
	var blocks []struct {
		Type       string               `json:"type"`
		Attachment nativeImageReference `json:"attachment"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, err
	}
	positions := []imagePosition{}
	for index, block := range blocks {
		if block.Type != contracts.DeepseekHarnessContentTypeImage {
			continue
		}
		ref, exists := refs[block.Attachment.ID]
		if !exists || ref != block.Attachment {
			return nil, errors.New("the DeepSeek Harness image position has another native reference")
		}
		position := index
		positions = append(positions, imagePosition{Position: &position, Attachment: ref})
	}
	return positions, nil
}

func equalImagePositions(left, right []imagePosition) bool {
	if len(left) != len(right) {
		return false
	}
	for index, position := range left {
		other := right[index]
		if position.Position == nil || other.Position == nil || *position.Position != *other.Position || position.Attachment != other.Attachment {
			return false
		}
	}
	return true
}

func receiptImageSupplement(receipt imageReceipt, sessionID, callID string, maximum int) ([]byte, error) {
	if maximum <= 0 || receipt.SessionID != sessionID || receipt.CallID != callID || receipt.ToolName == "" || receipt.IsError == nil || receipt.RetainedImages == nil || len(receipt.OriginalImages) == 0 {
		return nil, errors.New("the DeepSeek Harness image receipt has no exact native execution identity")
	}
	refs := map[string]nativeImageReference{}
	previous := -1
	for _, position := range receipt.OriginalImages {
		ref := position.Attachment
		if position.Position == nil || *position.Position <= previous || !nativeImageReferenceValid(ref) {
			return nil, errors.New("the DeepSeek Harness image receipt has an invalid native position or reference")
		}
		previous = *position.Position
		if earlier, exists := refs[ref.ID]; exists && earlier != ref {
			return nil, errors.New("the repeated DeepSeek Harness image references disagree")
		}
		refs[ref.ID] = ref
	}
	previous = -1
	for _, position := range *receipt.RetainedImages {
		original, exists := refs[position.Attachment.ID]
		if position.Position == nil || *position.Position <= previous || !exists || original != position.Attachment {
			return nil, errors.New("the DeepSeek Harness retained image has another original reference")
		}
		previous = *position.Position
	}
	images := map[string]imageValue{}
	for _, value := range receipt.Images {
		ref, exists := refs[value.Attachment.ID]
		if !exists || value.Attachment != ref {
			return nil, errors.New("the DeepSeek Harness saved image has another native reference")
		}
		if _, duplicate := images[ref.ID]; duplicate {
			return nil, errors.New("the DeepSeek Harness saved image has duplicate provenance")
		}
		if len(value.Data) > maximum {
			return nil, errors.New("the DeepSeek Harness saved image exceeds the message size limit")
		}
		data, err := base64.StdEncoding.DecodeString(value.Data)
		if err != nil || base64.StdEncoding.EncodeToString(data) != value.Data || int64(len(data)) != ref.Bytes {
			return nil, errors.New("the DeepSeek Harness saved image has invalid native bytes")
		}
		sum := sha256.Sum256(data)
		if ref.ID != "sha256:"+hex.EncodeToString(sum[:]) {
			return nil, errors.New("the DeepSeek Harness saved image changed after its native execution")
		}
		images[ref.ID] = value
	}
	if len(images) != len(refs) {
		return nil, errors.New("the DeepSeek Harness image receipt omits native image bytes")
	}
	raw, err := json.Marshal(map[string]any{
		contracts.DeepseekHarnessImageReceiptFieldSessionID:      sessionID,
		contracts.DeepseekHarnessImageReceiptFieldToolCallID:     callID,
		contracts.DeepseekHarnessImageReceiptFieldOriginalImages: receipt.OriginalImages,
		contracts.DeepseekHarnessImageReceiptFieldRetainedImages: *receipt.RetainedImages,
		contracts.DeepseekHarnessImageReceiptFieldImages:         images,
	})
	if err != nil {
		return nil, err
	}
	if len(raw) > maximum {
		return nil, errors.New("the DeepSeek Harness image receipt exceeds the message size limit")
	}
	return raw, nil
}

// recoverImageReceipt reads the private image receipt. It never opens an output text path.
func recoverImageReceipt(ctx context.Context, files imageHookFiles, sessionID, callID, toolName string, original []byte) ([]byte, error) {
	var event struct {
		Type string `json:"type"`
		Data struct {
			Message struct {
				CallID  string          `json:"toolCallId"`
				IsError *bool           `json:"isError"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(original, &event) != nil || event.Type != contracts.DeepseekHarnessEventToolResult || event.Data.Message.CallID != callID || event.Data.Message.IsError == nil || sessionID == "" || callID == "" || toolName == "" {
		return nil, nil
	}
	maximum := agent.LiveMaxMessageSize() - len(original) - 1024
	if maximum <= 0 {
		return nil, errors.New("the DeepSeek Harness image result exceeds the message size limit")
	}
	raw, err := readImageReceipt(ctx, files.Receipts, imageReceiptLeaf(sessionID, callID), maximum)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	var receipt imageReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("the DeepSeek Harness image receipt contains another JSON value")
	}
	if receipt.SessionID != sessionID || receipt.CallID != callID || receipt.ToolName != toolName || receipt.IsError == nil || *receipt.IsError != *event.Data.Message.IsError || receipt.RetainedImages == nil {
		return nil, errors.New("the DeepSeek Harness image receipt has another execution identity")
	}
	retained, err := nativeImagePositions(event.Data.Message.Content)
	if err != nil {
		return nil, err
	}
	if !equalImagePositions(*receipt.RetainedImages, retained) {
		return nil, errors.New("the DeepSeek Harness image receipt differs from its retained native images")
	}
	supplement, err := receiptImageSupplement(receipt, sessionID, callID, maximum)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]json.RawMessage{contracts.DeepseekHarnessSupplementImageAttachments: supplement})
}
