package agent

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestJSONRPCControlFailureRetainsTheWriteStage(t *testing.T) {
	for _, written := range []int{0, 2} {
		cause := errors.New("the pipe closed")
		connection := &jsonrpcBase{processBase: processBase{stdin: partialStdinWriter{written: written, err: cause}}}
		_, err := connection.sendRequest("control.answer", json.RawMessage(`{}`), time.Second)
		require.Error(t, err)
		err = classifyJSONRPCDeliveryError("control response", err)
		require.ErrorIs(t, err, cause)
		require.Equal(t, written > 0, errors.Is(err, ErrDeliveryUncertain))
	}
}
