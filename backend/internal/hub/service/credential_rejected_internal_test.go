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
	"github.com/leapmux/leapmux/internal/hub/store"
)

// connect.NewError takes a string and serializes only that. The helper's whole
// job is to send exactly what v1 sent while keeping the cause attached, so
// errors.Is and errors.As keep answering the way v1's
// `connect.NewError(code, err)` did.
func TestWrappedConnectError_SendsTheCauseTextAndKeepsTheCause(t *testing.T) {
	sentinel := errors.New("boom")
	cause := fmt.Errorf("count workers: %w", sentinel)

	cerr := wrappedConnectError(connect.CodeInternal, cause)

	assert.Equal(t, connect.CodeInternal, cerr.Code())
	assert.Equal(t, "count workers: boom", cerr.Message(),
		"the caller must read the same bytes v1 sent")
	require.ErrorIs(t, cerr, sentinel,
		"the wrapped cause must stay reachable, or every errors.Is consumer breaks")
	require.ErrorIs(t, cerr, cause)
}

// An error that wraps nothing is the common case, and it must survive the
// same way.
func TestWrappedConnectError_KeepsAnUnwrappedCauseReachable(t *testing.T) {
	cause := errors.New("boom")

	cerr := wrappedConnectError(connect.CodeUnauthenticated, cause)

	assert.Equal(t, "boom", cerr.Message())
	require.ErrorIs(t, cerr, cause)
}

// A caller switches on a TYPED cause. Losing it turns a specific refusal into
// a generic one with nothing to tell the two apart, which is how a counted
// admission refusal goes uncounted.
type testLimitError struct{ limit int64 }

func (e *testLimitError) Error() string { return fmt.Sprintf("worker cap %d", e.limit) }

func TestWrappedConnectError_KeepsATypedCauseReachable(t *testing.T) {
	cause := &testLimitError{limit: 3}

	cerr := wrappedConnectError(connect.CodeResourceExhausted, cause)

	assert.Equal(t, "worker cap 3", cerr.Message())
	var target *testLimitError
	require.ErrorAs(t, cerr, &target, "errors.As must reach the typed cause")
	assert.Equal(t, int64(3), target.limit)
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

type causeProbeStore struct {
	store.Store
	failure error
}

func (s causeProbeStore) OAuthProviders() store.OAuthProviderStore {
	return causeProbeProviders{failure: s.failure}
}

func (s causeProbeStore) RevocationEvents() store.RevocationEventStore {
	return causeProbeRevocations{failure: s.failure}
}

type causeProbeProviders struct {
	store.OAuthProviderStore
	failure error
}

func (s causeProbeProviders) ListAll(context.Context) ([]store.OAuthProviderSummary, error) {
	return nil, s.failure
}

type causeProbeRevocations struct {
	store.RevocationEventStore
	failure error
}

func (s causeProbeRevocations) SessionWasRevoked(context.Context, string) (bool, error) {
	return false, s.failure
}

func TestServiceReadFailures_PreserveTheTypedCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		call func(context.Context, store.Store) error
	}{
		{name: "provider list", text: "list the configured OAuth providers: worker cap 3", call: func(ctx context.Context, st store.Store) error {
			providers, err := enabledProviderIDs(ctx, st)
			assert.Nil(t, providers)
			return err
		}},
		{name: "revocation query", text: "check whether the acting session was revoked: worker cap 3", call: func(ctx context.Context, st store.Store) error {
			return refuseIfSessionWasRevoked(ctx, st, "session-1")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := &testLimitError{limit: 3}
			err := tc.call(t.Context(), causeProbeStore{failure: cause})
			assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
			var rpcErr *connect.Error
			require.ErrorAs(t, err, &rpcErr)
			assert.Equal(t, tc.text, rpcErr.Message())
			var typed *testLimitError
			require.ErrorAs(t, err, &typed)
			assert.Same(t, cause, typed)
		})
	}
}

func TestIssueAPIToken_PreservesTheScopeParserCause(t *testing.T) {
	st := newStepUpTestStore(t)
	user := stepUpUser(t, st, false)
	_, err := st.OAuthClients().Create(t.Context(), store.CreateOAuthClientParams{
		ClientID: "corrupt-app", ClientName: "Corrupt app", Scopes: "unknown:scope",
		RegistrationSource: store.OAuthClientSourceAdmin,
	})
	require.NoError(t, err)
	svc := &AdminUserService{store: st}
	_, err = svc.IssueAPIToken(t.Context(), &leapmuxv1.IssueAPITokenRequest{
		User: SelectUserByID(user.ID), ClientId: "corrupt-app", InstallationName: "test",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var rpcErr *connect.Error
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, `app corrupt-app carries an unreadable scope ceiling: unknown scope "unknown:scope"`, rpcErr.Message())
	formatted := errors.Unwrap(rpcErr)
	require.NotNil(t, formatted)
	parsed := errors.Unwrap(formatted)
	require.NotNil(t, parsed)
	assert.EqualError(t, parsed, `unknown scope "unknown:scope"`)
}
