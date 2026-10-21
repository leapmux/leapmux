package providerkit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONRPCRequestContextAlreadyCancelledWritesNothing(t *testing.T) {
	t.Parallel()
	stdin := &agenttest.Stdin{}
	connection := newObserverTestProcess(t, stdin)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := connection.SendRequestContext(ctx, "cancelled", nil, 0)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, errors.Is(ClassifyJSONRPCDeliveryError("request", err), agent.ErrDeliveryUncertain))
	assert.Empty(t, stdin.String())
	assert.Zero(t, connection.nextReqID.Load())
}

func TestJSONRPCRequestContextCancelsOnlyItsOwnRequest(t *testing.T) {
	t.Parallel()
	written := make(chan jsonrpcMessage, 2)
	connection := newObserverTestProcess(t, observerRequestWriter(func(raw []byte) (int, error) {
		var request jsonrpcMessage
		if err := json.Unmarshal(raw, &request); err != nil {
			return 0, err
		}
		written <- request
		return len(raw), nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	firstResult := make(chan error, 1)
	go func() {
		_, err := connection.SendRequestContext(ctx, "first", nil, 0)
		firstResult <- err
	}()
	first := <-written
	type reply struct {
		raw json.RawMessage
		err error
	}
	secondResult := make(chan reply, 1)
	go func() {
		raw, err := connection.SendRequestContext(t.Context(), "second", nil, 0)
		secondResult <- reply{raw, err}
	}()
	second := <-written
	cancel()
	require.ErrorIs(t, <-firstResult, context.Canceled)
	assert.False(t, connection.Deliver(first.ID, json.RawMessage(`{"result":"late"}`)))
	require.NoError(t, connection.Context().Err())
	require.True(t, connection.Deliver(second.ID, json.RawMessage(`{"result":{"value":9007199254740993}}`)))
	result := <-secondResult
	require.NoError(t, result.err)
	assert.JSONEq(t, `{"value":9007199254740993}`, string(result.raw))
	assert.False(t, connection.Deliver(second.ID, json.RawMessage(`{"result":"duplicate"}`)))
}
