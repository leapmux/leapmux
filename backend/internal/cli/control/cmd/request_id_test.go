package cmd

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/id"
)

type stubHintedErr struct {
	err   error
	hints []string
}

func (e *stubHintedErr) Error() string      { return e.err.Error() }
func (e *stubHintedErr) Unwrap() error      { return e.err }
func (e *stubHintedErr) CLIHints() []string { return e.hints }

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

func TestEnsureRequestID_TrimsWhitespaceAndGeneratesDefault(t *testing.T) {
	runEnsure := func(args ...string) (string, bool, error) {
		var (
			gotID       string
			gotExplicit bool
			gotErr      error
		)
		leaf := cli.NewLeaf(cli.Leaf{
			Use:   "test",
			Short: "test command",
			Flags: addRequestIDFlag,
			Run: func(inv *cli.Invocation) (any, error) {
				gotID, gotExplicit, gotErr = ensureRequestID(inv)
				return nil, nil
			},
		})
		root := cli.Group("leapmux", "test root", leaf)
		var out, errOut bytes.Buffer
		_ = cli.Run(context.Background(), root, append([]string{"test"}, args...), strings.NewReader(""), &out, &errOut)
		return gotID, gotExplicit, gotErr
	}

	// Trimmed explicit request-id matches the untrimmed version so send and request-status derive identical input_id hashes.
	trimmedID, explicit, err := runEnsure("--request-id", "  req-1 \t")
	require.NoError(t, err)
	assert.True(t, explicit)
	assert.Equal(t, "req-1", trimmedID)
	assert.Equal(t,
		requestScopedID("req-1", requestScopeAgentSend, "agent-1"),
		requestScopedID(trimmedID, requestScopeAgentSend, "agent-1"),
	)

	// Omitted flag generates a fresh non-empty ID with explicit == false.
	gen1, explicit1, err := runEnsure()
	require.NoError(t, err)
	assert.False(t, explicit1)
	assert.Len(t, gen1, id.GeneratedLength)

	gen2, explicit2, err := runEnsure()
	require.NoError(t, err)
	assert.False(t, explicit2)
	assert.NotEqual(t, gen1, gen2)

	// Whitespace-only explicit value fails with CodeUsage.
	_, explicitEmpty, err := runEnsure("--request-id", "   ")
	require.Error(t, err)
	assert.True(t, explicitEmpty)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "--request-id is empty")
}

func TestWithRequestIDHint(t *testing.T) {
	conflict := connect.NewError(connect.CodeFailedPrecondition, "queued agent input conflicts with existing data")

	wrapped := withRequestIDHint(&codedRPCError{Code: "rpc_error", Cause: conflict}, "nightly-1")
	var eager *cli.Error
	assert.False(t, errors.As(wrapped, &eager), "withRequestIDHint must not eagerly convert to *cli.Error")

	e := cli.FromError(wrapped)
	assert.Equal(t, cli.CodeConflict, e.Code)
	require.Len(t, e.Hints, 1)
	assert.Contains(t, e.Hints[0], `--request-id "nightly-1"`)

	// Wrapping a HintProvider must not duplicate inner hints on classification.
	hintedConflict := &stubHintedErr{err: conflict, hints: []string{"inner transport hint"}}
	eHinted := cli.FromError(withRequestIDHint(hintedConflict, "nightly-1"))
	assert.Equal(t, cli.CodeConflict, eHinted.Code)
	assert.Equal(t, 1, countStrings(eHinted.Hints, "inner transport hint"))
	require.Len(t, eHinted.Hints, 2)

	plain := &codedRPCError{Code: "rpc_error", Cause: conflict}
	assert.Same(t, plain, withRequestIDHint(plain, ""), "without a request id the error stays as it is")

	notFound := cli.Newf(cli.CodeNotFound, "no agent")
	assert.Same(t, notFound, withRequestIDHint(notFound, "nightly-1"), "a request id explains only a conflict")
	assert.Empty(t, notFound.Hints)
	assert.NoError(t, withRequestIDHint(nil, "nightly-1"))
}

func countStrings(items []string, want string) int {
	n := 0
	for _, s := range items {
		if s == want {
			n++
		}
	}
	return n
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
