package deepseekharness

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type nativeImageReference struct {
	ID        string `json:"attachmentId"`
	MediaType string `json:"mediaType"`
	Bytes     int64  `json:"bytes"`
	Width     int64  `json:"width"`
	Height    int64  `json:"height"`
}

// UnmarshalJSON reads the five fields that identify a native image and ignores every other
// field of the native reference, such as the optional display name and the original dimensions.
// The receipt decoder refuses unknown fields of the receipt, which LeapMux owns. The native
// reference inside it is a native object, and the native process can state more of it. The
// native image hook compares the same five fields.
func (r *nativeImageReference) UnmarshalJSON(data []byte) error {
	type identity nativeImageReference
	var value identity
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = nativeImageReference(value)
	return nil
}

func nativeResultImages(original []byte) (string, map[string]nativeImageReference, error) {
	var event struct {
		Type string `json:"type"`
		Data struct {
			Message struct {
				ID      string          `json:"toolCallId"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(original, &event) != nil || event.Type != contracts.DeepseekHarnessEventToolResult {
		return "", nil, nil
	}
	images, err := nativeImageReferences(event.Data.Message.Content)
	return event.Data.Message.ID, images, err
}

func nativeImageReferences(content []byte) (map[string]nativeImageReference, error) {
	var blocks []struct {
		Type       string          `json:"type"`
		Attachment json.RawMessage `json:"attachment"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return nil, fmt.Errorf("DeepSeek Harness returned invalid native image content")
	}
	images := map[string]nativeImageReference{}
	for _, block := range blocks {
		if block.Type != contracts.DeepseekHarnessContentTypeImage {
			continue
		}
		var ref nativeImageReference
		if json.Unmarshal(block.Attachment, &ref) != nil || len(ref.ID) != 71 || ref.ID[:7] != "sha256:" || ref.Bytes <= 0 || ref.Width <= 0 || ref.Height <= 0 {
			return nil, fmt.Errorf("DeepSeek Harness returned an invalid native image reference")
		}
		if !nativeImageReferenceValid(ref) {
			return nil, fmt.Errorf("DeepSeek Harness returned an unsupported native image type")
		}
		if previous, exists := images[ref.ID]; exists && previous != ref {
			return nil, fmt.Errorf("DeepSeek Harness image references disagree about their native metadata")
		}
		images[ref.ID] = ref
	}
	return images, nil
}

// recoverResultImages uses the native API's Session-log authorization before it reads bytes.
func (a *Agent) recoverResultImages(ctx context.Context, sessionID string, original []byte) ([]byte, error) {
	callID, refs, err := nativeResultImages(original)
	if err != nil || len(refs) == 0 {
		return nil, err
	}
	if sessionID == "" || callID == "" {
		return nil, fmt.Errorf("the DeepSeek Harness image result has no Session or call identity")
	}
	maximum := agent.LiveMaxMessageSize() - len(original) - 1024
	if maximum <= 0 {
		return nil, fmt.Errorf("the DeepSeek Harness image result exceeds the message size limit")
	}
	images := map[string]json.RawMessage{}
	for id, expected := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := a.rpc.value(ctx, "session/attachment", map[string]any{"request": map[string]string{"sessionId": sessionID, "attachmentId": id}})
		if err != nil {
			return nil, err
		}
		var value struct {
			Attachment nativeImageReference `json:"attachment"`
			Data       string               `json:"data"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Attachment != expected || len(value.Data) > maximum {
			return nil, fmt.Errorf("the DeepSeek Harness image value does not match its exact native reference")
		}
		data, err := base64.StdEncoding.DecodeString(value.Data)
		if err != nil || base64.StdEncoding.EncodeToString(data) != value.Data || int64(len(data)) != expected.Bytes {
			return nil, fmt.Errorf("the DeepSeek Harness image value has invalid native bytes")
		}
		sum := sha256.Sum256(data)
		if id != "sha256:"+hex.EncodeToString(sum[:]) {
			return nil, fmt.Errorf("the DeepSeek Harness image bytes do not match their native digest")
		}
		images[id] = raw
	}
	raw, err := json.Marshal(map[string]any{contracts.DeepseekHarnessSupplementImageAttachments: map[string]any{"sessionId": sessionID, "toolCallId": callID, "images": images}})
	if err != nil {
		return nil, err
	}
	if len(raw) > maximum {
		return nil, fmt.Errorf("the complete DeepSeek Harness images exceed the message size limit")
	}
	return raw, nil
}

func nativeImageReferenceValid(ref nativeImageReference) bool {
	return len(ref.ID) == 71 && ref.ID[:7] == "sha256:" && ref.Bytes > 0 && ref.Width > 0 && ref.Height > 0 &&
		(ref.MediaType == "image/png" || ref.MediaType == "image/jpeg" || ref.MediaType == "image/webp" || ref.MediaType == "image/gif")
}
