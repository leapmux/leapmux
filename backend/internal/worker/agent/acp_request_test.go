//go:build unix

package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPUnsupportedRequest(t *testing.T) {
	for _, id := range []string{`0`, `9007199254740993`, `"request-1"`, `""`} {
		t.Run(id, func(t *testing.T) {
			for _, storageError := range []error{nil, errors.New("storage unavailable")} {
				sink := &testSink{persistErr: storageError}
				base, recorder := newTerminalTestBase(t, sink)
				raw := []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"_vendor/request","params":{"value":1}}`)
				base.handleACPOutput(parseLine(raw), nil, nil)
				recorder.mu.Lock()
				responses := append([][]byte(nil), recorder.bufs...)
				recorder.mu.Unlock()
				require.Len(t, responses, 1)
				var response struct {
					ID    json.RawMessage `json:"id"`
					Error struct {
						Code int `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(responses[0], &response))
				assert.Equal(t, id, string(response.ID))
				assert.Equal(t, -32601, response.Error.Code)
				messages := sink.Messages()
				require.Len(t, messages, 1)
				assert.Equal(t, raw, messages[0].Content)
			}
		})
	}
}

func TestACPUnknownNotificationAndClaimedRequest(t *testing.T) {
	for _, raw := range []string{
		`{"jsonrpc":"2.0","method":"_vendor/update","params":{}}`,
		`{"jsonrpc":"2.0","id":null,"method":"_vendor/update","params":{}}`,
	} {
		sink := &testSink{}
		base, recorder := newTerminalTestBase(t, sink)
		base.handleACPOutput(parseLine([]byte(raw)), nil, nil)
		assert.Empty(t, recorder.bufs)
		require.Len(t, sink.Messages(), 1)
		assert.Equal(t, raw, string(sink.Messages()[0].Content))
	}
	sink := &testSink{}
	base, recorder := newTerminalTestBase(t, sink)
	claimed := false
	base.handleACPOutput(parseLine([]byte(`{"jsonrpc":"2.0","id":1,"method":"_vendor/request"}`)), nil, func(*parsedLine) bool {
		claimed = true
		return true
	})
	assert.True(t, claimed)
	assert.Empty(t, recorder.bufs)
	assert.Empty(t, sink.Messages())
}

func TestACPElicitationPublishesNativeRequest(t *testing.T) {
	sink := &recordingControlSink{}
	base, recorder := newTerminalTestBase(t, &sink.testSink)
	base.sink = sink
	raw := []byte(`{"jsonrpc":"2.0","id":"form-1","method":"elicitation/create","params":{"mode":"form","message":"Choose","requestedSchema":{"type":"object","properties":{}}}}`)
	base.handleACPOutput(parseLine(raw), nil, nil)
	requests := sink.publishedControls
	require.Len(t, requests, 1)
	assert.Equal(t, "form-1", requests[0].RequestID)
	assert.Equal(t, raw, requests[0].Payload)
	assert.Empty(t, recorder.bufs)
}
