package cmd

import (
	"regexp"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/id"
)

func TestRequestScopedID(t *testing.T) {
	derived := requestScopedID("nightly-1", requestScopeAgentSend, "agent-1")
	assert.Equal(t, derived, requestScopedID("nightly-1", requestScopeAgentSend, "agent-1"), "a retry derives the same id")
	assert.Len(t, derived, id.GeneratedLength, "a derived id has the length of a generated id")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]+$`), derived, "a derived id uses only the characters that every id uses")

	for name, other := range map[string]string{
		"another request id": requestScopedID("nightly-2", requestScopeAgentSend, "agent-1"),
		"another scope":      requestScopedID("nightly-1", requestScopeAgentOpen, "agent-1"),
		"another target":     requestScopedID("nightly-1", requestScopeAgentSend, "agent-2"),
		"no target":          requestScopedID("nightly-1", requestScopeAgentSend),
	} {
		assert.NotEqual(t, derived, other, name)
	}
	// Each part carries its length, so a boundary that moves between two
	// parts gives another id.
	assert.NotEqual(t, requestScopedID("ab", requestScopeAgentOpen, "c"), requestScopedID("a", requestScopeAgentOpen, "bc"))
	assert.NotEqual(t, requestScopedID("a:1:b", requestScopeAgentOpen), requestScopedID("a", requestScopeAgentOpen, "b"))

	fresh := requestScopedID("", requestScopeAgentSend, "agent-1")
	assert.Len(t, fresh, id.GeneratedLength)
	assert.NotEqual(t, fresh, requestScopedID("", requestScopeAgentSend, "agent-1"), "without a request id each call takes a fresh id")
}

func TestWithRequestIDHint(t *testing.T) {
	conflict := connect.NewError(connect.CodeFailedPrecondition, "queued agent input conflicts with existing data")

	e := cli.FromError(withRequestIDHint(&codedRPCError{Code: "rpc_error", Cause: conflict}, "nightly-1"))
	assert.Equal(t, cli.CodeConflict, e.Code)
	require.Len(t, e.Hints, 1)
	assert.Contains(t, e.Hints[0], `--request-id "nightly-1"`)

	plain := &codedRPCError{Code: "rpc_error", Cause: conflict}
	assert.Same(t, plain, withRequestIDHint(plain, ""), "without a request id the error stays as it is")

	notFound := cli.Newf(cli.CodeNotFound, "no agent")
	assert.Same(t, notFound, withRequestIDHint(notFound, "nightly-1"), "a request id explains only a conflict")
	assert.Empty(t, notFound.Hints)
	assert.NoError(t, withRequestIDHint(nil, "nightly-1"))
}

func TestRequestIDFlag_RefusesAnEmptyValue(t *testing.T) {
	clearRemoteEnv(t)
	for _, args := range [][]string{
		{"agent", "send", fullTestID("ag-1"), "hi", "--request-id", ""},
		{"agent", "send", fullTestID("ag-1"), "hi", "--request-id", "  "},
		{"agent", "open", "--request-id", ""},
	} {
		f := runControl(t, append(args, "--hub", "https://stub.invalid")...).requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--request-id is empty", args)
	}
}
