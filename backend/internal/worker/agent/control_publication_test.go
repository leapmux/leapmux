package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONRPCControlPublicationFailurePreservesWireID(t *testing.T) {
	t.Parallel()
	for _, wireID := range []string{`7`, `"007"`, `9007199254740993`} {
		t.Run(wireID, func(t *testing.T) {
			output := &syncBuffer{}
			sink := &recordingControlSink{publicationError: errors.New("storage unavailable")}
			base := jsonrpcBase{processBase: processBase{agentID: "agent", stdin: nopWriteCloser{output}}}
			base.publishControlRequest(sink, []byte(`{"jsonrpc":"2.0","id":`+wireID+`,"method":"request"}`), nil)
			// publishControlRequest runs on the goroutine that drains the provider's
			// stdout, so it QUEUES this reply rather than waiting for the write.
			var answer string
			require.Eventually(t, func() bool {
				answer = output.String()
				return answer != ""
			}, 2*time.Second, 5*time.Millisecond, "the publication failure is answered")
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":`+wireID+`,"error":{"code":-32603,"message":"LeapMux could not store this control request."}}`, answer)
			assert.Empty(t, sink.PublishedControls())
		})
	}
}

func TestJSONRPCControlPublicationDoesNotReplyOnSuccess(t *testing.T) {
	t.Parallel()
	output := &syncBuffer{}
	sink := &recordingControlSink{}
	base := jsonrpcBase{processBase: processBase{stdin: nopWriteCloser{output}}}
	payload := []byte(` {"jsonrpc":"2.0", "id":"007","method":"request"} `)
	base.publishControlRequest(sink, payload, nil)
	// Never, not a bare Empty: a reply travels on the stdin writer, so an assertion
	// straight after this call cannot tell "no reply" from "not written yet".
	require.Never(t, func() bool { return output.String() != "" },
		200*time.Millisecond, 5*time.Millisecond, "a published request draws no reply")
	require.Len(t, sink.PublishedControls(), 1)
	assert.Equal(t, payload, sink.LastPublishedControl().Payload)
}

func TestJSONRPCControlPublicationFailureWithoutWireIDDoesNotReply(t *testing.T) {
	t.Parallel()
	output := &syncBuffer{}
	sink := &recordingControlSink{publicationError: errors.New("storage unavailable")}
	base := jsonrpcBase{processBase: processBase{stdin: nopWriteCloser{output}}}
	base.publishControlRequest(sink, []byte(`{"method":"notification"}`), nil)
	require.Never(t, func() bool { return output.String() != "" },
		200*time.Millisecond, 5*time.Millisecond, "a frame with no wire id draws no reply")
}

func TestJSONRPCControlPublicationHandlesReplyFailure(t *testing.T) {
	t.Parallel()
	sink := &recordingControlSink{publicationError: errors.New("storage unavailable")}
	base := jsonrpcBase{processBase: processBase{stdin: failingWriteCloser{}}}
	require.NotPanics(t, func() {
		base.publishControlRequest(sink, []byte(`{"id":7,"method":"request"}`), nil)
	})
	assert.Empty(t, sink.PublishedControls())
}
