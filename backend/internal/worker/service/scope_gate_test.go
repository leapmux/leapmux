package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// dispatchScoped is dispatchAs with an explicit GRANT, so a probe can hold
// exactly one permission and nothing else. dispatchAs wraps its caller in
// LocalAgentCaller, which is unscoped by design and therefore cannot refuse.
func dispatchScoped(d *channel.Dispatcher, scopes authscope.ScopeSet, method string, req proto.Message, w *testResponseWriter) {
	payload, err := proto.Marshal(req)
	if err != nil {
		panic(err)
	}
	d.DispatchWith(context.Background(), channel.NewCaller(userid.MustNew("user-1"), scopes), &leapmuxv1.InnerRpcRequest{
		Method:  method,
		Payload: payload,
	}, w)
}

// The scope gate refuses one named neighbour, at dispatch, inside the worker.
//
// terminal:read versus terminal:write is the most important boundary in the
// vocabulary -- reading output is a monitoring dashboard, typing into a shell
// is arbitrary code execution -- and git:read versus git:write is its twin on
// the repository surface. The hub-side table cannot assert these: SendInput
// and PushBranch are worker inner-RPCs that no Connect procedure names, so
// the dispatch level is the only place the refusal exists at all.
func TestScopeGateRefusesTheNamedNeighbourAtDispatch(t *testing.T) {
	t.Parallel()

	one := func(scopes ...leapmuxv1.Scope) authscope.ScopeSet { return authscope.MustNew(scopes...) }

	t.Run("terminal:read does not reach SendInput", func(t *testing.T) {
		_, d, w := setupTestService(t)
		dispatchScoped(d, one(leapmuxv1.Scope_SCOPE_TERMINAL_READ), "SendInput",
			&leapmuxv1.SendInputRequest{TerminalId: "term-1", Data: []byte("ls")}, w)
		require.Len(t, w.errors, 1)
		assert.EqualValues(t, codes.PermissionDenied, w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "terminal:write",
			"the refusal must name the permission the caller lacks")
	})

	t.Run("terminal:write reaches the SendInput handler", func(t *testing.T) {
		_, d, w := setupTestService(t)
		dispatchScoped(d, one(leapmuxv1.Scope_SCOPE_TERMINAL_WRITE), "SendInput",
			&leapmuxv1.SendInputRequest{TerminalId: "no-such-terminal", Data: []byte("ls")}, w)
		require.Len(t, w.errors, 1)
		assert.NotEqualValues(t, codes.PermissionDenied, w.errors[0].code,
			"the handler itself must answer -- here, about a terminal that does not exist")
	})

	t.Run("git:read does not reach PushBranch", func(t *testing.T) {
		_, d, w := setupTestService(t)
		dispatchScoped(d, one(leapmuxv1.Scope_SCOPE_GIT_READ), "PushBranch",
			&leapmuxv1.PushBranchRequest{WorkingDir: "/tmp/repo"}, w)
		require.Len(t, w.errors, 1)
		assert.EqualValues(t, codes.PermissionDenied, w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "git:write")
	})

	t.Run("git:write reaches the PushBranch handler", func(t *testing.T) {
		_, d, w := setupTestService(t)
		dispatchScoped(d, one(leapmuxv1.Scope_SCOPE_GIT_WRITE), "PushBranch",
			&leapmuxv1.PushBranchRequest{WorkingDir: "/no/such/dir"}, w)
		require.Len(t, w.errors, 1)
		assert.NotEqualValues(t, codes.PermissionDenied, w.errors[0].code,
			"the handler itself must answer -- here, about a directory that is not a live tab's working dir")
	})

	// worker:read is implied by every other worker-surface scope, so a caller
	// holding the WRITE side alone still passes this gate via the closure --
	// and a caller holding only scopes the method does not need is still
	// refused, which is what keeps the gate a membership test rather than a
	// "some permission" test.
	t.Run("an unrelated permission reaches nothing", func(t *testing.T) {
		_, d, w := setupTestService(t)
		dispatchScoped(d, one(leapmuxv1.Scope_SCOPE_FILE_READ), "SendInput",
			&leapmuxv1.SendInputRequest{TerminalId: "term-1", Data: []byte("ls")}, w)
		require.Len(t, w.errors, 1)
		assert.EqualValues(t, codes.PermissionDenied, w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "terminal:write")
	})
}
