package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestStoreConnectError pins the one mapping from a store failure to a Connect
// code. Every answer identifies the call that failed, and each answer that
// wraps its cause keeps the cause inspectable.
func TestStoreConnectError(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		cause error
		code  connect.Code
	}{
		"a missing row is the caller's selector":   {fmt.Errorf("read: %w", store.ErrNotFound), connect.CodeNotFound},
		"a conflict is the caller's field":         {fmt.Errorf("write: %w", store.ErrConflict), connect.CodeAlreadyExists},
		"a bad cursor is the caller's input":       {fmt.Errorf("decode: %w", store.ErrInvalidCursor), connect.CodeInvalidArgument},
		"a refused argument is the caller's input": {fmt.Errorf("bind: %w", store.ErrInvalidArgument), connect.CodeInvalidArgument},
		"any other failure is a hub fault":         {errors.New("disk full"), connect.CodeInternal},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := storeConnectError(tc.cause, "list things")
			assert.Equal(t, tc.code, connect.CodeOf(err))
			assert.Contains(t, err.Error(), "list things", "the message identifies the call")
		})
	}

	// The two answers that carry the cause keep it inspectable.
	refused := fmt.Errorf("bind: %w", store.ErrInvalidArgument)
	assert.ErrorIs(t, storeConnectError(refused, "list things"), store.ErrInvalidArgument)
	fault := errors.New("disk full")
	internal := storeConnectError(fault, "list things")
	assert.ErrorIs(t, internal, fault, "the hub fault keeps its cause")
	assert.NotContains(t, internal.Error(), "omit it to start from the first page",
		"a hub fault does not tell the caller to change the cursor")
}

func TestStoreConnectError_PreservesTheInvalidCursorCause(t *testing.T) {
	cause := fmt.Errorf("cursor payload: %w", store.ErrInvalidCursor)
	err := storeConnectError(cause, "list users")
	require.ErrorIs(t, err, store.ErrInvalidCursor)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	var rpcErr *connect.Error
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, "list users: "+cause.Error()+" (pass the cursor printed at the end of the previous page, or omit it to start from the first page)", rpcErr.Message())
}

// TestListingsAnswerAStoreInvalidArgumentAsInvalidArgument pins that the three
// per-user listings classify a store failure as every other handler does:
// through storeConnectError, so a parameter the store refuses is the caller's
// input, not a hub fault.
func TestListingsAnswerAStoreInvalidArgumentAsInvalidArgument(t *testing.T) {
	t.Parallel()

	st := invalidArgumentListingStore{}
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew("u-1")})
	for name, list := range map[string]func() error{
		"ListWorkers": func() error {
			_, err := (&WorkerManagementService{store: st}).ListWorkers(ctx, &leapmuxv1.ListWorkersRequest{})
			return err
		},
		"ListApps": func() error {
			_, err := (&AppService{store: st}).ListApps(ctx, &leapmuxv1.ListAppsRequest{})
			return err
		},
		"ListMyAPITokens": func() error {
			_, err := (&UserService{store: st}).ListMyAPITokens(ctx, &leapmuxv1.ListMyAPITokensRequest{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := list()
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.ErrorIs(t, err, store.ErrInvalidArgument, "the cause stays inspectable")
		})
	}
}

// invalidArgumentListingStore fails the three per-user listings with
// store.ErrInvalidArgument, as a store does for a parameter it cannot use.
type invalidArgumentListingStore struct{ store.Store }

func (invalidArgumentListingStore) Workers() store.WorkerStore { return invalidArgumentWorkers{} }

func (invalidArgumentListingStore) OAuthClients() store.OAuthClientStore {
	return invalidArgumentOAuthClients{}
}

func (invalidArgumentListingStore) APITokens() store.APITokenStore { return invalidArgumentAPITokens{} }

var errListingInvalidArgument = fmt.Errorf("listing parameter: %w", store.ErrInvalidArgument)

type invalidArgumentWorkers struct{ store.WorkerStore }

func (invalidArgumentWorkers) ListByUserID(context.Context, store.ListWorkersByUserIDParams) (store.Page[store.Worker], error) {
	return store.Page[store.Worker]{}, errListingInvalidArgument
}

type invalidArgumentOAuthClients struct{ store.OAuthClientStore }

func (invalidArgumentOAuthClients) List(context.Context, store.ListOAuthClientsParams) (store.Page[store.OAuthClient], error) {
	return store.Page[store.OAuthClient]{}, errListingInvalidArgument
}

type invalidArgumentAPITokens struct{ store.APITokenStore }

func (invalidArgumentAPITokens) ListByUser(context.Context, store.ListAPITokensByUserParams) (store.Page[store.APIToken], error) {
	return store.Page[store.APIToken]{}, errListingInvalidArgument
}
