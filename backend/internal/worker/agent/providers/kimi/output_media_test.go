package kimi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kimiMediaFixtureItem(t *testing.T, itemID string, fileIDs ...string) json.RawMessage {
	t.Helper()
	parts := make([]map[string]string, 0, len(fileIDs))
	for _, id := range fileIDs {
		parts = append(parts, map[string]string{"kind": "image", "file_id": id})
	}
	raw, err := json.Marshal(map[string]any{
		"type": "tool_call", "item_id": itemID, "name": "ReadMediaFile",
		"status": "ended", "outcome": "completed", "created_at": "2026-11-18T00:00:00Z", "started_at": "2026-11-18T00:00:00Z",
		"input": map[string]string{"path": "original.png"}, "output": "Loaded original.png", "media": parts,
	})
	require.NoError(t, err)
	return raw
}

func readKimiMediaFixture(t *testing.T, api *kimiClient, originalSize int, fileIDs ...string) ([]byte, error) {
	t.Helper()
	plan, err := prepareKimiConversationMedia(contracts.KimiConversationRecord{
		Type: contracts.KimiConversationRecordKindItem, Role: contracts.KimiConversationRecordRoleResult,
		SessionID: "session_1", AgentID: "main", TurnID: "turn", Item: kimiMediaFixtureItem(t, "image", fileIDs...),
	}, originalSize)
	if err != nil {
		return nil, err
	}
	require.NotNil(t, plan)
	return plan.read(testutil.DeadlineContext(t), api, make(chan struct{}, kimiMediaConcurrency))
}

func TestKimiNativeMediaResultRetainsExactServerBytes(t *testing.T) {
	t.Parallel()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aY9sAAAAASUVORK5CYII=")
	require.NoError(t, err)
	mimeType := "image/png"
	hash := sha256.New()
	_, err = hash.Write([]byte(mimeType + "\x00"))
	require.NoError(t, err)
	_, err = hash.Write(data)
	require.NoError(t, err)
	fileID := "f_" + hex.EncodeToString(hash.Sum(nil))
	rig := newKimiConversationRig(t)
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+fakeKapToken, r.Header.Get("Authorization"))
		assert.Equal(t, "/api/v1/sessions/session_1/media/"+fileID, r.URL.Path)
		w.Header().Set("Content-Type", mimeType)
		_, _ = w.Write(data)
	})
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+string(kimiMediaFixtureItem(t, "image", fileID))+`}`)
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 2 }, "the native request and image result persist")
	messages := rig.sink.Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[1].Content), fileID)
	assert.NotContains(t, string(messages[1].Content), base64.StdEncoding.EncodeToString(data))
	require.NotEmpty(t, messages[1].SupplementalContent)
	assert.Contains(t, string(messages[1].SupplementalContent), fileID)
	assert.Contains(t, string(messages[1].SupplementalContent), base64.StdEncoding.EncodeToString(data))
}

func TestKimiNativeMediaRejectsInvalidResponses(t *testing.T) {
	t.Parallel()
	data := []byte("native image bytes")
	digest := sha256.Sum256(append([]byte("image/png\x00"), data...))
	validID := "f_" + hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name, id, mimeType, body string
		status                   int
		expected                 string
	}{
		{"digest mismatch", validID, "image/png", "changed bytes", 200, "digest"},
		{"unsupported MIME", validID, "text/plain", string(data), 200, "MIME"},
		{"missing MIME", validID, "", string(data), 200, "MIME"},
		{"empty bytes", validID, "image/png", "", 200, "no bytes"},
		{"HTTP refusal", validID, "image/png", "refused", 403, "403"},
		{"invalid ID", "../../secret", "image/png", string(data), 200, "media ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			var requests atomic.Int32
			rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, "Bearer "+fakeKapToken, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", tc.mimeType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			supplement, err := readKimiMediaFixture(t, rig.agent.api, len("original"), tc.id)
			require.ErrorContains(t, err, tc.expected)
			assert.Empty(t, supplement)
			if tc.name == "invalid ID" {
				assert.Zero(t, requests.Load())
			}
		})
	}
}

func TestKimiNativeMediaFailurePreservesOriginalResult(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	id := "f_" + strings.Repeat("a", 64)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+string(kimiMediaFixtureItem(t, "image", id))+`}`)
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 2 }, "the native request and image result persist")
	messages := rig.sink.Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[1].Content), id)
	assert.Empty(t, messages[1].SupplementalContent)
}

func TestKimiNativeMediaEnforcesMessageSize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, data string
		available  int
		expected   string
	}{
		{"no remaining bytes", "image", 0, "available message space"},
		{"binary read limit", "more than six bytes", 8, "remaining message size limit"},
		{"supplement overhead", "abc", 8, "exceeds the message size limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			digest := sha256.Sum256(append([]byte("image/png\x00"), []byte(tc.data)...))
			id := "f_" + hex.EncodeToString(digest[:])
			rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write([]byte(tc.data))
			})
			originalSize := agent.LiveMaxMessageSize() - 1024 - tc.available
			supplement, err := readKimiMediaFixture(t, rig.agent.api, originalSize, id)
			require.ErrorContains(t, err, tc.expected)
			assert.Empty(t, supplement)
		})
	}
}

func TestKimiNativeMediaFetchesRepeatedReferenceOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	data := []byte("exact native bytes")
	digest := sha256.Sum256(append([]byte("image/png\x00"), data...))
	id := "f_" + hex.EncodeToString(digest[:])
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	supplement, err := readKimiMediaFixture(t, rig.agent.api, len("original"), id, id)
	require.NoError(t, err)
	assert.Contains(t, string(supplement), base64.StdEncoding.EncodeToString(data))
	assert.EqualValues(t, 1, requests.Load())
}

func TestKimiNativeMediaAcceptsNativeImageMIMETypes(t *testing.T) {
	t.Parallel()
	for _, mimeType := range []string{"image/png", "image/bmp", "image/avif", "image/tiff", "image/svg+xml"} {
		t.Run(mimeType, func(t *testing.T) {
			assert.True(t, kimiIsImageMIMEType(mimeType), "native image results are separate from attachment input formats")
		})
	}
}
