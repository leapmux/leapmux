package codewhale

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	stdimage "image"
	"image/png"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The installed native probe returned this complete 64 by 64 PNG.
const codewhaleMediaPNG = "iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAYAAACqaXHeAAAAb0lEQVR42u3YMREAIAwEwZcYicjBFSigykwatjgDW15S63wdAAAAAAAAAODdTi8AAAAAAAAAAAAAAAAAAAAAgCECAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAJjqArCUycOeoJLSAAAAAElFTkSuQmCC"

func TestToolMediaPersistsVerifiedOutputFileBytesBesideTheRawEvent(t *testing.T) {
	t.Parallel()
	data, err := base64.StdEncoding.DecodeString(codewhaleMediaPNG)
	require.NoError(t, err)
	digest := sha256.Sum256(data)
	revision := hex.EncodeToString(digest[:])
	sessionID := "23601ee5-bd86-4058-a0e1-aec92016f3ce"
	outputFileID := "art_image_" + strings.Repeat("a", 64)
	callID := "media-call"
	path := "/v1/sessions/" + sessionID + "/artifacts/" + outputFileID
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodGet, path, http.StatusOK, map[string]any{
		"artifact": map[string]any{
			"id": outputFileID, "session_id": sessionID, "content_type": "image/png",
			"kind": "tool_output", "tool_call_id": callID, "tool_name": "read_media",
			"created_at": "2026-10-01T00:00:00Z", "byte_size": len(data), "preview": "",
			"path": "artifacts/" + outputFileID + ".image",
		},
		"size": len(data), "revision": revision, "offset": 0, "bytes": len(data),
		"truncated": false, "encoding": "base64", "content": codewhaleMediaPNG,
	})
	a, sink := newTestAgent(t, rt)
	input := map[string]any{"path": "shot.png"}
	a.HandleOutput(toolStartEvent(1, "media-item", callID, contracts.CodewhaleToolReadMedia, input))
	end := toolEndEvent(2, "item.completed", "media-item", callID, contracts.CodewhaleToolReadMedia, "The native image is ready.", input, map[string]any{
		"tool_media": []any{map[string]any{
			"version": 1, "session_id": sessionID, "artifact_id": outputFileID, "tool_call_id": callID,
			"media_type": "image/png", "byte_size": len(data), "sha256": revision, "width": 64, "height": 64,
		}},
	})
	a.HandleOutput(end)
	rows := messagesWithSpan(sink, callID)
	require.Len(t, rows, 2)
	assert.Equal(t, end, rows[1].Content)
	assert.True(t, rows[1].Closing)
	require.NotEmpty(t, rows[1].SupplementalContent, "the actual image bytes must survive runtime shutdown and transcript reopen")
	var supplement struct {
		OutputFiles map[string]string `json:"outputFiles"`
	}
	require.NoError(t, json.Unmarshal(rows[1].SupplementalContent, &supplement))
	assert.Equal(t, "data:image/png;base64,"+codewhaleMediaPNG, supplement.OutputFiles[outputFileID])
	requests := rt.requestsTo(http.MethodGet, path)
	require.Len(t, requests, 1)
	assert.Equal(t, "Bearer "+testToken, requests[0].Auth)
}

func mediaFixture(t *testing.T) (mediaDescriptor, map[string]any) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(codewhaleMediaPNG)
	require.NoError(t, err)
	digest := sha256.Sum256(data)
	descriptor := mediaDescriptor{Version: 1, SessionID: "native_session-1", OutputFileID: "art_image_" + strings.Repeat("b", 64), ToolCallID: "media-boundary", MediaType: "image/png", ByteSize: len(data), SHA256: hex.EncodeToString(digest[:]), Width: 64, Height: 64}
	response := map[string]any{"artifact": map[string]any{"id": descriptor.OutputFileID, "session_id": descriptor.SessionID, "content_type": descriptor.MediaType, "kind": "tool_output", "tool_call_id": descriptor.ToolCallID, "tool_name": "read_media", "byte_size": descriptor.ByteSize}, "size": descriptor.ByteSize, "revision": descriptor.SHA256, "offset": 0, "bytes": descriptor.ByteSize, "truncated": false, "encoding": "base64", "content": codewhaleMediaPNG}
	return descriptor, response
}

func TestMediaDescriptorRejectsInvalidNativeFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(*mediaDescriptor)
	}{
		{"version zero", func(d *mediaDescriptor) { d.Version = 0 }},
		{"version negative", func(d *mediaDescriptor) { d.Version = -1 }},
		{"version newer", func(d *mediaDescriptor) { d.Version = 2 }},
		{"session absent", func(d *mediaDescriptor) { d.SessionID = "" }},
		{"session traversal", func(d *mediaDescriptor) { d.SessionID = "../native" }},
		{"session unicode", func(d *mediaDescriptor) { d.SessionID = "native-é" }},
		{"artifact absent", func(d *mediaDescriptor) { d.OutputFileID = "" }},
		{"artifact foreign prefix", func(d *mediaDescriptor) { d.OutputFileID = "file_" + strings.Repeat("b", 64) }},
		{"artifact short", func(d *mediaDescriptor) { d.OutputFileID = "art_image_abc" }},
		{"artifact nonhex", func(d *mediaDescriptor) { d.OutputFileID = "art_image_" + strings.Repeat("z", 64) }},
		{"foreign call", func(d *mediaDescriptor) { d.ToolCallID = "another-call" }},
		{"unsupported MIME", func(d *mediaDescriptor) { d.MediaType = "image/svg+xml" }},
		{"zero size", func(d *mediaDescriptor) { d.ByteSize = 0 }},
		{"negative size", func(d *mediaDescriptor) { d.ByteSize = -1 }},
		{"excessive size", func(d *mediaDescriptor) { d.ByteSize = contracts.CodewhaleMediaRuleMaxImageBytes + 1 }},
		{"missing hash", func(d *mediaDescriptor) { d.SHA256 = "" }},
		{"upper hash", func(d *mediaDescriptor) { d.SHA256 = strings.Repeat("A", 64) }},
		{"zero width", func(d *mediaDescriptor) { d.Width = 0 }},
		{"negative height", func(d *mediaDescriptor) { d.Height = -1 }},
		{"excessive width", func(d *mediaDescriptor) { d.Width = contracts.CodewhaleMediaRuleMaxDimension + 1 }},
		{"excessive pixels", func(d *mediaDescriptor) { d.Width = 8192; d.Height = 8192 }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			descriptor, _ := mediaFixture(t)
			tt.change(&descriptor)
			assert.False(t, descriptor.valid("media-boundary"))
		})
	}
}

func TestToolMediaRefusesInvalidDescriptorsBeforeHTTP(t *testing.T) {
	t.Parallel()
	for _, value := range []any{nil, []any{}, "not descriptors", 42, []any{map[string]any{}}, []any{map[string]any{"version": 1.5}}, []any{map[string]any{}, map[string]any{}}} {
		t.Run(fmt.Sprintf("%T-%v", value, value), func(t *testing.T) {
			t.Parallel()
			rt := newFakeRuntime(t)
			a, sink := newTestAgent(t, rt)
			end := toolEndEvent(2, "item.completed", "media-item", "media-boundary", contracts.CodewhaleToolReadMedia, "The native result remains.", nil, map[string]any{"tool_media": value})
			a.HandleOutput(end)
			rows := messagesWithSpan(sink, "media-boundary")
			require.Len(t, rows, 1)
			assert.Equal(t, end, rows[0].Content)
			assert.Empty(t, rows[0].SupplementalContent)
			rt.mu.Lock()
			defer rt.mu.Unlock()
			assert.Empty(t, rt.requests)
		})
	}
}

func TestToolMediaPreservesRawResultOnInvalidOutputFileReply(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"foreign artifact", func(r map[string]any) { r["artifact"].(map[string]any)["id"] = "another-artifact" }},
		{"foreign session", func(r map[string]any) { r["artifact"].(map[string]any)["session_id"] = "another-session" }},
		{"foreign call", func(r map[string]any) { r["artifact"].(map[string]any)["tool_call_id"] = "another-call" }},
		{"foreign tool", func(r map[string]any) { r["artifact"].(map[string]any)["tool_name"] = "another-tool" }},
		{"wrong MIME", func(r map[string]any) { r["artifact"].(map[string]any)["content_type"] = "image/jpeg" }},
		{"wrong kind", func(r map[string]any) { r["artifact"].(map[string]any)["kind"] = "file" }},
		{"wrong artifact size", func(r map[string]any) { r["artifact"].(map[string]any)["byte_size"] = 1 }},
		{"wrong total size", func(r map[string]any) { r["size"] = 1 }},
		{"wrong revision", func(r map[string]any) { r["revision"] = strings.Repeat("0", 64) }},
		{"wrong offset", func(r map[string]any) { r["offset"] = 1 }},
		{"missing offset", func(r map[string]any) { delete(r, "offset") }},
		{"zero progress", func(r map[string]any) { r["bytes"] = 0 }},
		{"too many bytes", func(r map[string]any) { r["bytes"] = 169 }},
		{"missing truncation", func(r map[string]any) { delete(r, "truncated") }},
		{"wrong truncation", func(r map[string]any) { r["truncated"] = true }},
		{"wrong encoding", func(r map[string]any) { r["encoding"] = "hex" }},
		{"utf8 mismatch", func(r map[string]any) { r["encoding"] = "utf-8" }},
		{"invalid base64", func(r map[string]any) { r["content"] = "!" }},
		{"noncanonical base64", func(r map[string]any) { r["content"] = codewhaleMediaPNG + "\n" }},
		{"short body", func(r map[string]any) { r["content"] = "AAAA" }},
		{"wrong bytes", func(r map[string]any) { r["content"] = base64.StdEncoding.EncodeToString(make([]byte, 168)) }},
		{"excessive JSON", func(r map[string]any) { r["extra"] = strings.Repeat("x", mediaReplyMetadataBytes+1024) }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			descriptor, response := mediaFixture(t)
			tt.change(response)
			rt := newFakeRuntime(t)
			path := "/v1/sessions/" + descriptor.SessionID + "/artifacts/" + descriptor.OutputFileID
			rt.respondJSON(http.MethodGet, path, http.StatusOK, response)
			a, sink := newTestAgent(t, rt)
			end := toolEndEvent(2, "item.completed", "media-item", descriptor.ToolCallID, contracts.CodewhaleToolReadMedia, "The native image is ready.", nil, map[string]any{"tool_media": []mediaDescriptor{descriptor}})
			a.HandleOutput(end)
			rows := messagesWithSpan(sink, descriptor.ToolCallID)
			require.Len(t, rows, 1)
			assert.Equal(t, end, rows[0].Content)
			assert.Empty(t, rows[0].SupplementalContent)
		})
	}
}

func TestToolMediaHandlesHTTPFailuresAndCancellation(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusFound} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			d, _ := mediaFixture(t)
			rt := newFakeRuntime(t)
			rt.respondStatus(http.MethodGet, "/v1/sessions/"+d.SessionID+"/artifacts/"+d.OutputFileID, status, "The fixture refused its artifact.")
			a, _ := newTestAgent(t, rt)
			raw, err := json.Marshal([]mediaDescriptor{d})
			require.NoError(t, err)
			assert.Empty(t, a.recoverToolMedia(itemMetadata{ToolMedia: raw}, d.ToolCallID, "read_media", 0))
		})
	}
	t.Run("cancelled actual HTTP read", func(t *testing.T) {
		t.Parallel()
		d, _ := mediaFixture(t)
		rt := newFakeRuntime(t)
		entered := make(chan struct{})
		cancelled := make(chan struct{})
		rt.handle(http.MethodGet, "/v1/sessions/"+d.SessionID+"/artifacts/"+d.OutputFileID, func(_ http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) })
		a, _ := newTestAgent(t, rt)
		ctx, cancel := context.WithCancel(a.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := a.readToolMedia(ctx, d, "read_media"); done <- err }()
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("The artifact request did not start.")
		}
		cancel()
		select {
		case err := <-done:
			assert.Error(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("The cancelled artifact read did not end.")
		}
		select {
		case <-cancelled:
		case <-time.After(30 * time.Second):
			t.Fatal("The native artifact request did not observe cancellation.")
		}
	})
}

func TestToolMediaReadsTwoNativeWindowsAndRejectsChangedWindows(t *testing.T) {
	t.Parallel()
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	require.NoError(t, encoder.Encode(&encoded, stdimage.NewGray(stdimage.Rect(0, 0, 4096, 1100))))
	data := encoded.Bytes()
	require.Greater(t, len(data), mediaReadWindowBytes)
	require.LessOrEqual(t, len(data), contracts.CodewhaleMediaRuleMaxImageBytes)
	digest := sha256.Sum256(data)
	for _, failure := range []string{"", "revision", "offset", "zero progress", "early truncation"} {
		t.Run("second window "+failure, func(t *testing.T) {
			t.Parallel()
			d, _ := mediaFixture(t)
			d.ByteSize = len(data)
			d.Width = 4096
			d.Height = 1100
			d.SHA256 = hex.EncodeToString(digest[:])
			rt := newFakeRuntime(t)
			path := "/v1/sessions/" + d.SessionID + "/artifacts/" + d.OutputFileID
			rt.handle(http.MethodGet, path, func(w http.ResponseWriter, r *http.Request) {
				offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
				require.NoError(t, err)
				limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
				require.NoError(t, err)
				require.Greater(t, limit, 0)
				require.LessOrEqual(t, limit, mediaReadWindowBytes)
				require.GreaterOrEqual(t, offset, 0)
				require.Less(t, offset, len(data))
				end := min(offset+limit, len(data))
				_, response := mediaFixture(t)
				response["artifact"].(map[string]any)["byte_size"] = d.ByteSize
				response["size"] = d.ByteSize
				response["revision"] = d.SHA256
				response["offset"] = offset
				response["bytes"] = end - offset
				response["truncated"] = end < len(data)
				response["content"] = base64.StdEncoding.EncodeToString(data[offset:end])
				if offset > 0 {
					switch failure {
					case "revision":
						response["revision"] = strings.Repeat("0", 64)
					case "offset":
						response["offset"] = offset + 1
					case "zero progress":
						response["bytes"] = 0
					case "early truncation":
						response["truncated"] = true
					}
				}
				writeFakeJSON(w, http.StatusOK, response)
			})
			a, _ := newTestAgent(t, rt)
			raw, err := json.Marshal([]mediaDescriptor{d})
			require.NoError(t, err)
			supplement := a.recoverToolMedia(itemMetadata{ToolMedia: raw}, d.ToolCallID, "read_media", 0)
			if failure != "" {
				assert.Empty(t, supplement)
			} else {
				var recovered mediaSupplement
				require.NoError(t, json.Unmarshal(supplement, &recovered))
				assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data), recovered.OutputFiles[d.OutputFileID])
			}
			requests := rt.requestsTo(http.MethodGet, path)
			require.Len(t, requests, 2)
			query, err := url.ParseQuery(requests[1].RawQuery)
			require.NoError(t, err)
			assert.Equal(t, strconv.Itoa(mediaReadWindowBytes), query.Get("offset"))
		})
	}
}

func TestToolMediaChecksActualEncodedTypeAndDimensions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		change func(*mediaDescriptor, map[string]any)
	}{
		{"wrong width", func(d *mediaDescriptor, _ map[string]any) { d.Width = 63 }},
		{"wrong height", func(d *mediaDescriptor, _ map[string]any) { d.Height = 63 }},
		{"wrong actual MIME", func(d *mediaDescriptor, r map[string]any) {
			d.MediaType = "image/jpeg"
			r["artifact"].(map[string]any)["content_type"] = d.MediaType
		}},
		{"invalid actual header", func(d *mediaDescriptor, r map[string]any) {
			data := make([]byte, d.ByteSize)
			hash := sha256.Sum256(data)
			d.SHA256 = hex.EncodeToString(hash[:])
			r["revision"] = d.SHA256
			r["content"] = base64.StdEncoding.EncodeToString(data)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d, response := mediaFixture(t)
			tt.change(&d, response)
			rt := newFakeRuntime(t)
			rt.respondJSON(http.MethodGet, "/v1/sessions/"+d.SessionID+"/artifacts/"+d.OutputFileID, http.StatusOK, response)
			a, _ := newTestAgent(t, rt)
			raw, err := json.Marshal([]mediaDescriptor{d})
			require.NoError(t, err)
			assert.Empty(t, a.recoverToolMedia(itemMetadata{ToolMedia: raw}, d.ToolCallID, "read_media", 0))
		})
	}
}

func TestToolMediaKeepsDifferentCallsIndependent(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, _ := newTestAgent(t, rt)
	descriptors := make([]mediaDescriptor, 2)
	for index := range descriptors {
		d, response := mediaFixture(t)
		d.ToolCallID = fmt.Sprintf("media-%d", index)
		d.OutputFileID = "art_image_" + strings.Repeat(strconv.Itoa(index), 64)
		response["artifact"].(map[string]any)["id"] = d.OutputFileID
		response["artifact"].(map[string]any)["tool_call_id"] = d.ToolCallID
		rt.respondJSON(http.MethodGet, "/v1/sessions/"+d.SessionID+"/artifacts/"+d.OutputFileID, http.StatusOK, response)
		descriptors[index] = d
	}
	results := make(chan []byte, 2)
	for _, d := range descriptors {
		go func() {
			raw, err := json.Marshal([]mediaDescriptor{d})
			if err != nil {
				results <- nil
				return
			}
			results <- a.recoverToolMedia(itemMetadata{ToolMedia: raw}, d.ToolCallID, "read_media", 0)
		}()
	}
	recovered := make(map[string]string)
	for range descriptors {
		select {
		case data := <-results:
			var supplement mediaSupplement
			require.NoError(t, json.Unmarshal(data, &supplement))
			require.Len(t, supplement.OutputFiles, 1)
			for id, value := range supplement.OutputFiles {
				recovered[id] = value
			}
		case <-time.After(30 * time.Second):
			t.Fatal("The concurrent artifact read did not end.")
		}
	}
	for _, d := range descriptors {
		assert.Equal(t, "data:image/png;base64,"+codewhaleMediaPNG, recovered[d.OutputFileID])
	}
}

func TestToolMediaRejectsMissingAndWrongTypedDescriptorFields(t *testing.T) {
	t.Parallel()
	descriptor, _ := mediaFixture(t)
	original, err := json.Marshal(descriptor)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(original, &fields))
	for key := range fields {
		for _, value := range []any{nil, "wrong", 1.5, "missing field"} {
			t.Run(fmt.Sprintf("%s-%v", key, value), func(t *testing.T) {
				t.Parallel()
				record := make(map[string]any)
				for field, current := range fields {
					record[field] = current
				}
				if value == "missing field" {
					delete(record, key)
				} else if key == "session_id" && value == "wrong" {
					// A valid ASCII session ID needs an HTTP read. Traversal must fail before that read.
					record[key] = "../wrong"
				} else {
					record[key] = value
				}
				raw, err := json.Marshal([]any{record})
				require.NoError(t, err)
				rt := newFakeRuntime(t)
				a, _ := newTestAgent(t, rt)
				assert.Empty(t, a.recoverToolMedia(itemMetadata{ToolMedia: raw}, descriptor.ToolCallID, "read_media", 0))
				rt.mu.Lock()
				defer rt.mu.Unlock()
				assert.Empty(t, rt.requests)
			})
		}
	}
}

func TestToolMediaCancelsItsActualReadDuringStop(t *testing.T) {
	t.Parallel()
	descriptor, _ := mediaFixture(t)
	rt := newFakeRuntime(t)
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	rt.handle(http.MethodGet, "/v1/sessions/"+descriptor.SessionID+"/artifacts/"+descriptor.OutputFileID, func(_ http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) })
	a, sink := newTestAgent(t, rt)
	end := toolEndEvent(2, "item.completed", "media-item", descriptor.ToolCallID, contracts.CodewhaleToolReadMedia, "The native completed result remains.", nil, map[string]any{"tool_media": []mediaDescriptor{descriptor}})
	dispatched := make(chan struct{})
	go func() { a.HandleOutput(end); close(dispatched) }()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("The native artifact read did not start.")
	}
	a.Stop()
	select {
	case <-dispatched:
	case <-time.After(30 * time.Second):
		t.Fatal("The completed image event did not finish after Stop.")
	}
	select {
	case <-cancelled:
	case <-time.After(30 * time.Second):
		t.Fatal("The native artifact read did not observe Stop.")
	}
	rows := messagesWithSpan(sink, descriptor.ToolCallID)
	require.Len(t, rows, 1)
	assert.Equal(t, end, rows[0].Content)
	assert.Empty(t, rows[0].SupplementalContent)
}

func TestToolMediaPreservesResultsOnMalformedJSONAndConnectionFailure(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{"{", "null", "[]"} {
		t.Run(reply, func(t *testing.T) {
			t.Parallel()
			descriptor, _ := mediaFixture(t)
			rt := newFakeRuntime(t)
			rt.handle(http.MethodGet, "/v1/sessions/"+descriptor.SessionID+"/artifacts/"+descriptor.OutputFileID, func(w http.ResponseWriter, _ *http.Request) { _, err := w.Write([]byte(reply)); assert.NoError(t, err) })
			a, _ := newTestAgent(t, rt)
			raw, err := json.Marshal([]mediaDescriptor{descriptor})
			require.NoError(t, err)
			assert.Empty(t, a.recoverToolMedia(itemMetadata{ToolMedia: raw}, descriptor.ToolCallID, "read_media", 0))
		})
	}
	t.Run("connection failure", func(t *testing.T) {
		t.Parallel()
		descriptor, _ := mediaFixture(t)
		rt := newFakeRuntime(t)
		a, _ := newTestAgent(t, rt)
		rt.server.Close()
		raw, err := json.Marshal([]mediaDescriptor{descriptor})
		require.NoError(t, err)
		assert.Empty(t, a.recoverToolMedia(itemMetadata{ToolMedia: raw}, descriptor.ToolCallID, "read_media", 0))
	})
}

func TestToolMediaPreservesTheLiveMessageByteBudget(t *testing.T) {
	t.Parallel()
	descriptor, _ := mediaFixture(t)
	rt := newFakeRuntime(t)
	a, _ := newTestAgent(t, rt)
	raw, err := json.Marshal([]mediaDescriptor{descriptor})
	require.NoError(t, err)
	for _, originalBytes := range []int{-1, agent.LiveMaxMessageSize(), agent.LiveMaxMessageSize() - mediaReplyMetadataBytes} {
		assert.Empty(t, a.recoverToolMedia(itemMetadata{ToolMedia: raw}, descriptor.ToolCallID, "read_media", originalBytes))
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	assert.Empty(t, rt.requests, "a known oversized supplement must not start native asset reads")
}

func TestToolMediaHandlesAnAbsentEndpoint(t *testing.T) {
	t.Parallel()
	descriptor, _ := mediaFixture(t)
	raw, err := json.Marshal([]mediaDescriptor{descriptor})
	require.NoError(t, err)
	a, _ := newTestAgent(t, nil)
	assert.Empty(t, a.recoverToolMedia(itemMetadata{ToolMedia: raw}, descriptor.ToolCallID, "read_media", 0))
	assert.Empty(t, a.recoverToolMedia(itemMetadata{}, descriptor.ToolCallID, "read_media", 0))
}

func TestToolMediaReadsAValidOutputFileSessionWithoutMatchingTheRuntimeThread(t *testing.T) {
	t.Parallel()
	descriptor, _ := mediaFixture(t)
	descriptor.SessionID = "wrong"
	raw, err := json.Marshal([]mediaDescriptor{descriptor})
	require.NoError(t, err)
	rt := newFakeRuntime(t)
	a, _ := newTestAgent(t, rt)
	assert.NotEqual(t, descriptor.SessionID, a.threadID)
	assert.True(t, descriptor.valid(descriptor.ToolCallID))
	assert.Empty(t, a.recoverToolMedia(itemMetadata{ToolMedia: raw}, descriptor.ToolCallID, "read_media", 0))
	requests := rt.requestsTo(http.MethodGet, "/v1/sessions/wrong/artifacts/"+descriptor.OutputFileID)
	require.Len(t, requests, 1, "a valid artifact session ID must reach its native route")
}
