package reasonix

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Native 2.x stores image slots beside each event's messages. A blob contains the complete
// data URL, and its filename is the SHA-256 of that URL, including its media-type prefix.
type reasonixEventImages struct {
	Message *int `json:"message"`
	Slots   []struct {
		Blob string `json:"blob"`
		URL  string `json:"url"`
	} `json:"slots"`
}

// restoreReasonixRecordImages reconstructs one native message without interpreting its images.
// Missing, damaged, oversized, or ambiguous image data leaves the tool's text available.
func restoreReasonixRecordImages(eventPath string, raw, blobs json.RawMessage, index int) json.RawMessage {
	var entries []reasonixEventImages
	if len(blobs) == 0 || json.Unmarshal(blobs, &entries) != nil {
		return raw
	}
	var selected *reasonixEventImages
	for i := range entries {
		if entries[i].Message == nil || *entries[i].Message != index {
			continue
		}
		if selected != nil {
			return raw
		}
		selected = &entries[i]
	}
	if selected == nil || len(selected.Slots) == 0 {
		return raw
	}
	var message map[string]json.RawMessage
	if json.Unmarshal(raw, &message) != nil || message == nil {
		return raw
	}
	if _, exists := message[contracts.ReasonixToolRecordImagesField]; exists {
		return raw
	}
	directory := strings.TrimSuffix(eventPath, ".events.jsonl") + ".blobs"
	remaining := agent.LiveMaxMessageSize() - len(raw) - len(contracts.ReasonixToolRecordImagesField) - 6
	images := []byte{'['}
	for _, slot := range selected.Slots {
		if remaining <= 0 || (slot.Blob != "" && slot.URL != "") {
			continue
		}
		url := slot.URL
		if slot.Blob != "" {
			url = readReasonixImageBlob(directory, slot.Blob, remaining)
		}
		if url == "" {
			continue
		}
		encoded, err := json.Marshal(url)
		if err != nil || len(encoded)+1 > remaining {
			continue
		}
		remaining -= len(encoded) + 1
		if len(images) > 1 {
			images = append(images, ',')
		}
		images = append(images, encoded...)
	}
	if len(images) == 1 {
		return raw
	}
	images = append(images, ']')
	message[contracts.ReasonixToolRecordImagesField] = json.RawMessage(images)
	restored, err := json.Marshal(message)
	if err != nil || len(restored) > agent.LiveMaxMessageSize() {
		return raw
	}
	return restored
}

// readReasonixImageBlob admits only a complete payload whose native content hash agrees.
func readReasonixImageBlob(directory, key string, maxBytes int) string {
	maxBytes = min(maxBytes, agent.LiveMaxMessageSize())
	hash, ok := strings.CutPrefix(key, "sha256:")
	if !ok || len(hash) != sha256.Size*2 || maxBytes <= 0 {
		return ""
	}
	decoded, err := hex.DecodeString(hash)
	if err != nil || hex.EncodeToString(decoded) != hash {
		return ""
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()
	data, err := sessionstore.ReadRegularFile(root, hash, int64(maxBytes))
	if err != nil {
		return ""
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != hash {
		return ""
	}
	return string(data)
}
