package kimi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	rig := newKimiOutputRig(t)
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+fakeKapToken, r.Header.Get("Authorization"))
		assert.Equal(t, "/api/v1/sessions/session_1/media/"+fileID, r.URL.Path)
		w.Header().Set("Content-Type", mimeType)
		_, _ = w.Write(data)
	})
	op := fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.image","toolCallId":"image","name":"ReadMediaFile","state":"done","input":{"path":"original.png"},"display":{"path":"/work/original.png"},"output":[{"type":"image","source":{"kind":"session_media","file_id":%q}}]}}`, fileID)
	rig.transcriptOps(t, 1, op)
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 1 }, "the native media result persists")
	messages := rig.sink.Messages()
	require.Equal(t, 1, len(messages))
	assert.Contains(t, string(messages[0].Content), fileID)
	assert.NotContains(t, string(messages[0].Content), base64.StdEncoding.EncodeToString(data))
	require.NotEmpty(t, messages[0].SupplementalContent)
	assert.Contains(t, string(messages[0].SupplementalContent), fileID)
	assert.Contains(t, string(messages[0].SupplementalContent), base64.StdEncoding.EncodeToString(data))
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
			rig := newKimiOutputRig(t)
			var requests atomic.Int32
			rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, "Bearer "+fakeKapToken, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", tc.mimeType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			frame, err := json.Marshal(map[string]any{"frameId": "frame", "toolCallId": "call", "output": []any{map[string]any{"type": "image", "source": map[string]string{"kind": "session_media", "file_id": tc.id}}}})
			require.NoError(t, err)
			supplement, err := rig.agent.recoverTranscriptMedia("session_1", "main", kimiTranscriptOperation{TurnID: "turn", StepID: "step", Frame: frame}, []byte("original"))
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
	rig := newKimiOutputRig(t)
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	id := "f_" + strings.Repeat("a", 64)
	op := fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"image","toolCallId":"image","name":"ReadMediaFile","state":"done","input":{"path":"original.png"},"output":[{"type":"image","source":{"kind":"session_media","file_id":%q}}]}}`, id)
	rig.transcriptOps(t, 1, op)
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 1 }, "the native media result persists")
	messages := rig.sink.Messages()
	require.Len(t, messages, 1)
	assert.Contains(t, string(messages[0].Content), id)
	assert.Empty(t, messages[0].SupplementalContent)
}

func TestKimiNativeMediaEnforcesMessageSize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, data string
		available  int
		expected   string
	}{
		{"no remaining bytes", "image", 0, "message size limit"},
		{"binary read limit", "more than six bytes", 8, "remaining message size limit"},
		{"supplement overhead", "abc", 8, "complete Kimi tool supplement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			digest := sha256.Sum256(append([]byte("image/png\x00"), []byte(tc.data)...))
			id := "f_" + hex.EncodeToString(digest[:])
			rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write([]byte(tc.data))
			})
			frame := json.RawMessage(fmt.Sprintf(`{"frameId":"frame","toolCallId":"call","output":[{"type":"image","source":{"kind":"session_media","file_id":%q}}]}`, id))
			original := make([]byte, agent.LiveMaxMessageSize()-1024-tc.available)
			supplement, err := rig.agent.recoverTranscriptMedia("session_1", "main", kimiTranscriptOperation{TurnID: "turn", StepID: "step", Frame: frame}, original)
			require.ErrorContains(t, err, tc.expected)
			assert.Empty(t, supplement)
		})
	}
}

func TestKimiNativeMediaFetchesRepeatedReferenceOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	data := []byte("exact native bytes")
	digest := sha256.Sum256(append([]byte("image/png\x00"), data...))
	id := "f_" + hex.EncodeToString(digest[:])
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	part := fmt.Sprintf(`{"type":"image","source":{"kind":"session_media","file_id":%q}}`, id)
	frame := json.RawMessage(fmt.Sprintf(`{"frameId":"frame","toolCallId":"call","output":[%s,%s]}`, part, part))
	supplement, err := rig.agent.recoverTranscriptMedia("session_1", "main", kimiTranscriptOperation{TurnID: "turn", StepID: "step", Frame: frame}, []byte("original"))
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
