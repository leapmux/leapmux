package deepseekharness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResultImagesUseTheExactNativeSessionAndContentDigest(t *testing.T) {
	t.Parallel()
	var pixels bytes.Buffer
	require.NoError(t, png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	sum := sha256.Sum256(pixels.Bytes())
	id := "sha256:" + hex.EncodeToString(sum[:])
	ref := nativeImageReference{ID: id, MediaType: "image/png", Bytes: int64(pixels.Len()), Width: 2, Height: 3}
	original, err := json.Marshal(map[string]any{"type": "tool/result", "seq": 1, "data": map[string]any{"message": map[string]any{"toolCallId": "image-call", "content": []any{map[string]any{"type": "image", "attachment": ref}}, "isError": false}}})
	require.NoError(t, err)
	for _, testCase := range []struct {
		name      string
		data      string
		reference nativeImageReference
		wantError string
	}{
		{name: "genuine native bytes", data: base64.StdEncoding.EncodeToString(pixels.Bytes()), reference: ref},
		{name: "invalid base64", data: "not base64", reference: ref, wantError: "invalid native bytes"},
		{name: "wrong bytes", data: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, pixels.Len())), reference: ref, wantError: "native digest"},
		{name: "different dimensions", data: base64.StdEncoding.EncodeToString(pixels.Bytes()), reference: nativeImageReference{ID: id, MediaType: "image/png", Bytes: int64(pixels.Len()), Width: 20, Height: 3}, wantError: "exact native reference"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID      string `json:"rpcId"`
					Payload struct {
						Args struct {
							Request struct {
								SessionID    string `json:"sessionId"`
								AttachmentID string `json:"attachmentId"`
							} `json:"request"`
						} `json:"args"`
					} `json:"payload"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Equal(t, "/api/session/attachment", r.URL.Path)
				assert.Equal(t, "native-root", request.Payload.Args.Request.SessionID)
				assert.Equal(t, id, request.Payload.Args.Request.AttachmentID)
				if err := json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": map[string]any{"attachment": testCase.reference, "data": testCase.data}}}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(server.Close)
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			t.Cleanup(endpoint.Close)
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.rpc.endpoint = endpoint
			extra, err := a.recoverResultImages(context.Background(), "native-root", original)
			if testCase.wantError != "" {
				require.ErrorContains(t, err, testCase.wantError)
				assert.Empty(t, extra)
				return
			}
			require.NoError(t, err)
			var supplement struct {
				Images struct {
					SessionID string                     `json:"sessionId"`
					CallID    string                     `json:"toolCallId"`
					Values    map[string]json.RawMessage `json:"images"`
				} `json:"imageAttachments"`
			}
			require.NoError(t, json.Unmarshal(extra, &supplement))
			assert.Equal(t, "native-root", supplement.Images.SessionID)
			assert.Equal(t, "image-call", supplement.Images.CallID)
			require.Contains(t, supplement.Images.Values, id)
		})
	}
}

func TestNativeResultImagesRejectsInvalidReferencesAndKeepsNonImageResults(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"type":"tool/result","data":{"message":{"content":[{"type":"image","attachment":null}]}}}`, `{"type":"tool/result","data":{"message":{"content":[{"type":"image","attachment":{"attachmentId":"short","mediaType":"image/png","bytes":0,"width":0,"height":-1}}]}}}`} {
		_, _, err := nativeResultImages([]byte(raw))
		require.Error(t, err)
	}
	id, images, err := nativeResultImages([]byte(`{"type":"tool/result","data":{"message":{"toolCallId":"text-only","content":[{"type":"text","text":"Native text"}]}}}`))
	require.NoError(t, err)
	assert.Equal(t, "text-only", id)
	assert.Empty(t, images)
}
