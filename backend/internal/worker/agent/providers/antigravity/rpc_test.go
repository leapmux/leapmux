package antigravity

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityFrameDecoding(t *testing.T) {
	first := []byte(`{"update":{"status":"CASCADE_RUN_STATUS_RUNNING"}}`)
	second := []byte(`{"update":{"status":"CASCADE_RUN_STATUS_IDLE"}}`)
	stream := append(encodeSingleFrame(first), encodeSingleFrame(second)...)
	stream = append(stream, 2, 0, 0, 0, 2, '{', '}')
	var frames [][]byte
	delivered, err := decodeStreamFrames(bytes.NewReader(stream), func(raw json.RawMessage) error {
		frames = append(frames, raw)
		return nil
	})
	assert.True(t, delivered)
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, [][]byte{first, second}, frames)
}

func TestAntigravityFrameRejectsTruncationAndOversize(t *testing.T) {
	_, err := decodeStreamFrames(bytes.NewReader([]byte{0, 0, 0, 0, 5, '{'}), func(json.RawMessage) error { return nil })
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[1:], antigravityMaxFrameBytes+1)
	_, err = decodeStreamFrames(bytes.NewReader(header), func(json.RawMessage) error { return nil })
	assert.True(t, isFrameOversized(err), "%v", err)
}

func TestAntigravityFrameEndError(t *testing.T) {
	frame := encodeSingleFrame([]byte(`{"error":{"code":"not_found","message":"missing"}}`))
	frame[0] = 2
	_, err := decodeStreamFrames(bytes.NewReader(frame), func(json.RawMessage) error { return nil })
	assert.True(t, isStreamNotFound(err), "%v", err)
}

func TestAntigravityReadStepCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, connectJSONContentType, r.Header.Get("Content-Type"))
		body, _ := io.ReadAll(r.Body)
		assert.Equal(t, []byte{0}, body[:1])
		var request map[string]any
		require.NoError(t, json.Unmarshal(body[5:], &request))
		assert.Equal(t, float64(pastEveryStep), request["initialStepsPageBounds"].(map[string]any)["startIndex"])
		w.Header().Set("Content-Type", connectJSONContentType)
		_, _ = w.Write(encodeSingleFrame([]byte(`{"update":{"mainTrajectoryUpdate":{"stepsUpdate":{"totalLength":17}}}}`)))
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	rpc := antigravityRPC{endpoint: endpoint, timeout: time.Second}
	count, err := rpc.readStepCount(t.Context(), "cascade")
	require.NoError(t, err)
	assert.Equal(t, 17, count)
}

func TestAntigravityReadStepCountNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frame := encodeSingleFrame([]byte(`{"error":{"code":"not_found"}}`))
		frame[0] = 2
		_, _ = w.Write(frame)
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	rpc := antigravityRPC{endpoint: endpoint, timeout: time.Second}
	_, err = rpc.readStepCount(t.Context(), "missing")
	assert.True(t, isStreamNotFound(err), "%v", err)
	assert.False(t, errors.Is(err, io.EOF))
}
