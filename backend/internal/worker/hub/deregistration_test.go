package hub

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestDeregistrationKeepsCredentialsOnUncertainOutcomes pins the client
// half of the uncertain-result rule: every outcome the hub did not
// confirm -- a refusal, a transport failure, a cancellation -- is an
// error, and only a confirmed empty response succeeds.
func TestDeregistrationKeepsCredentialsOnUncertainOutcomes(t *testing.T) {
	t.Run("a confirmed deletion succeeds", func(t *testing.T) {
		mock := &mockConnectorClient{
			deregisterFn: func(context.Context, *leapmuxv1.DeregisterSelfRequest) (*leapmuxv1.DeregisterSelfResponse, error) {
				return &leapmuxv1.DeregisterSelfResponse{}, nil
			},
		}
		require.NoError(t, deregisterWithClient(context.Background(), mock, "token"))
	})

	t.Run("a refusal is an error, never proof of deletion", func(t *testing.T) {
		mock := &mockConnectorClient{
			deregisterFn: func(context.Context, *leapmuxv1.DeregisterSelfRequest) (*leapmuxv1.DeregisterSelfResponse, error) {
				return nil, connect.Errorf(connect.CodeUnauthenticated, "unknown token")
			},
		}
		err := deregisterWithClient(context.Background(), mock, "token")
		require.Error(t, err)
		assert.NotErrorIs(t, err, errConfirmedDeletion{})
	})

	t.Run("a transport failure is an error", func(t *testing.T) {
		mock := &mockConnectorClient{
			deregisterFn: func(context.Context, *leapmuxv1.DeregisterSelfRequest) (*leapmuxv1.DeregisterSelfResponse, error) {
				return nil, connect.Errorf(connect.CodeUnavailable, "hub down")
			},
		}
		require.Error(t, deregisterWithClient(context.Background(), mock, "token"))
	})

	t.Run("a cancellation keeps both causes", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		mock := &mockConnectorClient{
			deregisterFn: func(context.Context, *leapmuxv1.DeregisterSelfRequest) (*leapmuxv1.DeregisterSelfResponse, error) {
				cancel()
				return nil, connect.Errorf(connect.CodeUnavailable, "mid-request loss")
			},
		}
		err := deregisterWithClient(ctx, mock, "token")
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.True(t, errors.As(err, new(*connect.Error)), "the hub's own failure rides along")
	})
}

// errConfirmedDeletion identifies the sentinel that does not exist: the
// client never treats any failure as a confirmed deletion.
type errConfirmedDeletion struct{}

func (errConfirmedDeletion) Error() string { return "confirmed deletion" }
