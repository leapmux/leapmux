package agent

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONRPCControlPublicationFailurePreservesWireID(t *testing.T) {
	t.Parallel()
	for _, wireID := range []string{`7`, `"007"`, `9007199254740993`} {
		t.Run(wireID, func(t *testing.T) {
			var output bytes.Buffer
			sink := &recordingControlSink{publicationError: errors.New("storage unavailable")}
			base := jsonrpcBase{processBase: processBase{agentID: "agent", stdin: nopWriteCloser{&output}}}
			base.publishControlRequest(sink, "request", []byte(`{"jsonrpc":"2.0","id":`+wireID+`,"method":"request"}`))
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":`+wireID+`,"error":{"code":-32603,"message":"LeapMux could not store this control request."}}`, output.String())
			assert.Empty(t, sink.PublishedControls())
		})
	}
}

func TestJSONRPCControlPublicationDoesNotReplyOnSuccess(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	sink := &recordingControlSink{}
	base := jsonrpcBase{processBase: processBase{stdin: nopWriteCloser{&output}}}
	payload := []byte(` {"jsonrpc":"2.0", "id":"007","method":"request"} `)
	base.publishControlRequest(sink, "007", payload)
	assert.Empty(t, output.String())
	require.Len(t, sink.PublishedControls(), 1)
	assert.Equal(t, payload, sink.LastPublishedControl().Payload)
}

func TestJSONRPCControlPublicationFailureWithoutWireIDDoesNotReply(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	sink := &recordingControlSink{publicationError: errors.New("storage unavailable")}
	base := jsonrpcBase{processBase: processBase{stdin: nopWriteCloser{&output}}}
	base.publishControlRequest(sink, "request", []byte(`{"method":"notification"}`))
	assert.Empty(t, output.String())
}

func TestJSONRPCControlPublicationHandlesReplyFailure(t *testing.T) {
	t.Parallel()
	sink := &recordingControlSink{publicationError: errors.New("storage unavailable")}
	base := jsonrpcBase{processBase: processBase{stdin: failingWriteCloser{}}}
	require.NotPanics(t, func() {
		base.publishControlRequest(sink, "7", []byte(`{"id":7,"method":"request"}`))
	})
	assert.Empty(t, sink.PublishedControls())
}
