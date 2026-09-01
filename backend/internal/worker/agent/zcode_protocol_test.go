package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyZCodeMessage_AllFourEnvelopeShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		line string
		want zcodeMessageKind
	}{
		{"response: id only", `{"id":7,"result":{}}`, zcodeMessageResponse},
		{"response: id and an error", `{"id":7,"error":{"code":-32004,"message":"gone"}}`, zcodeMessageResponse},
		{"server request: id and method", `{"id":3,"method":"interaction/requestPermission","params":{}}`, zcodeMessageServerRequest},
		{"notification: method only", `{"method":"session/event","params":{}}`, zcodeMessageNotification},
		{"unknown: neither", `{"hello":"world"}`, zcodeMessageUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, classifyZCodeMessage(parseLine([]byte(tc.line))))
		})
	}
}

// A response to one of OUR requests can itself carry a method-looking field, and an
// inbound request always carries both. The shape alone therefore cannot decide, and
// this test pins the documented contract: classify reports the SHAPE, and the read
// loop resolves the race by asking the correlator first.
func TestClassifyZCodeMessage_IDPlusMethodIsShapeOnly(t *testing.T) {
	t.Parallel()

	line := parseLine([]byte(`{"id":42,"method":"interaction/requestUserInput","params":{}}`))
	assert.Equal(t, zcodeMessageServerRequest, classifyZCodeMessage(line))

	a := newZCodeTestAgent(t, &recordingControlSink{})
	// A pending request registered under the same id must win, so the app-server's
	// answer is never mistaken for the request it answers.
	ch, release := a.register(int64(42))
	defer release()
	assert.True(t, a.interceptResponse(line), "a pending id must consume the line before the server-request path")
	select {
	case got := <-ch:
		assert.JSONEq(t, `{"id":42,"method":"interaction/requestUserInput","params":{}}`, string(got))
	default:
		t.Fatal("the pending request received nothing")
	}
}

func TestParseZCodeEvent_BothSpellings(t *testing.T) {
	t.Parallel()

	t.Run("flat params, as a session/event notification sends it", func(t *testing.T) {
		t.Parallel()
		event, ok := parseZCodeEvent([]byte(`{"eventId":"e1","sessionId":"s1","seq":9,"type":"turn.started","payload":{"turnNumber":1}}`))
		require.True(t, ok)
		assert.Equal(t, contracts.ZCodeEventTurnStarted, event.Type)
		assert.Equal(t, int64(9), event.Seq)
		assert.Equal(t, "e1", event.EventID)
		assert.Equal(t, "s1", event.SessionID)
		assert.JSONEq(t, `{"turnNumber":1}`, string(event.Payload))
	})

	t.Run("nested under event, as a replay returns it", func(t *testing.T) {
		t.Parallel()
		event, ok := parseZCodeEvent([]byte(`{"event":{"seq":4,"type":"turn.completed","payload":{"toolCallCount":2}}}`))
		require.True(t, ok)
		assert.Equal(t, contracts.ZCodeEventTurnCompleted, event.Type)
		assert.Equal(t, int64(4), event.Seq)
	})

	t.Run("a typed flat envelope wins over an empty nested one", func(t *testing.T) {
		t.Parallel()
		event, ok := parseZCodeEvent([]byte(`{"type":"tool.updated","seq":2,"event":{}}`))
		require.True(t, ok)
		assert.Equal(t, contracts.ZCodeEventToolUpdated, event.Type)
	})

	for _, tc := range []struct {
		name   string
		params string
	}{
		{"empty params", ``},
		{"null params", `null`},
		{"not an object", `[1,2,3]`},
		{"no type anywhere", `{"seq":3,"payload":{}}`},
		{"nested with no type", `{"event":{"seq":3}}`},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			t.Parallel()
			_, ok := parseZCodeEvent(json.RawMessage(tc.params))
			assert.False(t, ok)
		})
	}
}

func TestZCodeErrorCode_OnlyUnwrapsWireErrors(t *testing.T) {
	t.Parallel()

	wire := &zcodeError{Code: ZCodeErrSessionNotActive, Message: "session is not active"}
	code, ok := zcodeErrorCode(fmt.Errorf("session/send: %w", wire))
	assert.True(t, ok, "a wrapped wire error must still be unwrapped")
	assert.Equal(t, ZCodeErrSessionNotActive, code)

	_, ok = zcodeErrorCode(fmt.Errorf("write to stdin: broken pipe"))
	assert.False(t, ok, "a transport failure carries no app-server code")

	_, ok = zcodeErrorCode(nil)
	assert.False(t, ok)
}

func TestZCodeIsPromptRunning_CoversBothCodes(t *testing.T) {
	t.Parallel()

	for _, code := range []int{ZCodeErrPromptRunning, ZCodeErrPromptRunningLegacy} {
		assert.Truef(t, zcodeIsPromptRunning(&zcodeError{Code: code}), "code %d must be retryable", code)
	}
	assert.False(t, zcodeIsPromptRunning(&zcodeError{Code: ZCodeErrSessionNotActive}))
	assert.False(t, zcodeIsPromptRunning(fmt.Errorf("timeout")))
	assert.False(t, zcodeIsPromptRunning(nil))
}

func TestZCodeIsMethodNotFound(t *testing.T) {
	t.Parallel()

	assert.True(t, zcodeIsMethodNotFound(&zcodeError{Code: ZCodeErrMethodNotFound}))
	assert.False(t, zcodeIsMethodNotFound(&zcodeError{Code: ZCodeErrInternal}))
	assert.False(t, zcodeIsMethodNotFound(fmt.Errorf("nope")))
}

func TestZCodeError_ErrorTextFallsBackWhenTheMessageIsEmpty(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "boom", (&zcodeError{Message: "boom"}).Error())
	assert.Equal(t, "zcode error", (&zcodeError{Code: -1}).Error())
	var nilErr *zcodeError
	assert.Equal(t, "", nilErr.Error(), "a nil wire error must not panic")
}

// `auto` is in the app-server's own mode enumeration and is not implemented in the
// shipped build. It must therefore have no constant that an option list could pick
// up by accident.
func TestZCodeModes_AutoIsNotAModeConstant(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{contracts.ZCodeModePlan, contracts.ZCodeModeBuild, contracts.ZCodeModeEdit, contracts.ZCodeModeYolo} {
		assert.NotEqual(t, "auto", mode)
	}
	assert.Equal(t, contracts.ZCodeModeBuild, contracts.ZCodeDefaultMode)
}
