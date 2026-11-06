package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recoveryRequired reads the retained recovery flag under its lock.
func recoveryRequired(a *Agent) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.authenticationRecoveryRequired
}

// inputChecked reads the one-shot account-check flag under its lock.
func inputChecked(a *Agent) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.inputAuthenticationChecked
}

// accountReadReply builds the account/read result the fake peer returns.
func accountReadReply(account string, requiresAuth bool) agenttest.RPCReply {
	var accountJSON string
	if account == "" {
		accountJSON = "null"
	} else {
		accountJSON = fmt.Sprintf(`{"type":"chatgpt","email":%q,"planType":"pro"}`, account+"@example.invalid")
	}
	return agenttest.RPCReply{Result: json.RawMessage(
		fmt.Sprintf(`{"account":%s,"requiresOpenaiAuth":%t}`, accountJSON, requiresAuth))}
}

// TestCodexAuthenticationFailureClassification covers the classifier: the
// native credential failures qualify, and everything that must keep its
// existing handling does not.
func TestCodexAuthenticationFailureClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message string
		info    string
		want    bool
	}{
		{
			name:    "native unauthorized",
			info:    `"unauthorized"`,
			message: "Your access token could not be refreshed because you have since logged out or signed in to another account. Please sign in again.",
			want:    true,
		},
		{
			name:    "refresh token expired",
			message: "Your access token could not be refreshed because your refresh token has expired. Please log out and sign in again.",
			want:    true,
		},
		{
			name:    "refresh token revoked",
			message: "Your access token could not be refreshed because your refresh token was revoked. Please log out and sign in again.",
			want:    true,
		},
		{
			name:    "refresh token reused",
			message: "Your access token could not be refreshed because your refresh token was already used. Please log out and sign in again.",
			want:    true,
		},
		{
			name:    "account mismatch",
			message: "Your access token could not be refreshed because you have since logged out or signed in to another account. Please sign in again.",
			want:    true,
		},
		{
			name:    "workspace routing discovery unauthorized",
			message: "workspace routing discovery unauthorized (401)",
			want:    true,
		},
		{
			name:    "http connection failed 401",
			info:    `{"httpConnectionFailed":{"httpStatusCode":401}}`,
			message: "unexpected status 401 Unauthorized",
			want:    true,
		},
		{
			name:    "response stream connection failed 401",
			info:    `{"responseStreamConnectionFailed":{"httpStatusCode":401}}`,
			message: "stream failed",
			want:    true,
		},
		{
			name: "response stream disconnected 401",
			info: `{"responseStreamDisconnected":{"httpStatusCode":401}}`,
			want: true,
		},
		{
			name: "too many failed attempts 401",
			info: `{"responseTooManyFailedAttempts":{"httpStatusCode":401}}`,
			want: true,
		},
		{
			name:    "http 403 with recognized refresh failure",
			info:    `{"httpConnectionFailed":{"httpStatusCode":403}}`,
			message: `unexpected status 403 Forbidden: {"error": {"code": "refresh_token_invalidated"}}, url: https://api.openai.com/v1/responses`,
			want:    true,
		},
		{
			name:    "bare http 403 is a permission failure",
			info:    `{"httpConnectionFailed":{"httpStatusCode":403}}`,
			message: "unexpected status 403 Forbidden",
			want:    false,
		},
		{
			name:    "http 403 with unrelated message",
			info:    `{"httpConnectionFailed":{"httpStatusCode":403}}`,
			message: "The request is not allowed",
			want:    false,
		},
		{
			name:    "http 429 rate limit",
			info:    `{"httpConnectionFailed":{"httpStatusCode":429}}`,
			message: "rate limited",
			want:    false,
		},
		{
			name:    "http 500 server error",
			info:    `{"httpConnectionFailed":{"httpStatusCode":500}}`,
			message: "internal error",
			want:    false,
		},
		{
			name:    "server overloaded variant",
			info:    `"serverOverloaded"`,
			message: "server overloaded",
			want:    false,
		},
		{
			name:    "other string variant",
			info:    `"contextWindowExceeded"`,
			message: "context window exceeded",
			want:    false,
		},
		{
			name:    "missing error details",
			message: "stream disconnected before completion",
			want:    false,
		},
		{
			name:    "null error details",
			info:    `null`,
			message: "some failure",
			want:    false,
		},
		{
			name:    "malformed details",
			info:    `{"httpConnectionFailed":{"httpStatusCode":"many"}}`,
			message: "some failure",
			want:    false,
		},
		{
			name:    "negative status",
			info:    `{"httpConnectionFailed":{"httpStatusCode":-401}}`,
			message: "some failure",
			want:    false,
		},
		{
			name:    "zero status",
			info:    `{"httpConnectionFailed":{"httpStatusCode":0}}`,
			message: "some failure",
			want:    false,
		},
		{
			name:    "excessively large status",
			info:    `{"httpConnectionFailed":{"httpStatusCode":70000}}`,
			message: "some failure",
			want:    false,
		},
		{
			name:    "empty status",
			info:    `{"httpConnectionFailed":{}}`,
			message: "some failure",
			want:    false,
		},
		{
			name:    "null status",
			info:    `{"httpConnectionFailed":{"httpStatusCode":null}}`,
			message: "some failure",
			want:    false,
		},
		{
			name:    "message mentioning auth without a credential failure",
			message: "The authentication middleware rejected an MCP server's OAuth login",
			want:    false,
		},
		{
			name: "empty everything",
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var info json.RawMessage
			if test.info != "" {
				info = json.RawMessage(test.info)
			}
			assert.Equal(t, test.want, codexAuthenticationFailure(test.message, info))
		})
	}
}

// TestCodexPrepareInputProcessChecksAccountOnce drives the preparation through
// the real account/read round trip for every answer shape.
func TestCodexPrepareInputProcessChecksAccountOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		reply       agenttest.RPCReply
		wantErr     bool
		wantMarked  bool
		wantChecked bool
	}{
		{
			name:        "account present",
			reply:       accountReadReply("account-1", true),
			wantChecked: true,
		},
		{
			name:        "auth not required",
			reply:       accountReadReply("", false),
			wantChecked: true,
		},
		{
			name:    "account absent while required",
			reply:   accountReadReply("", true),
			wantErr: true, wantMarked: true,
		},
		{
			name:    "missing requiresOpenaiAuth",
			reply:   agenttest.RPCReply{Result: json.RawMessage(`{"account":null}`)},
			wantErr: true,
		},
		{
			name:    "malformed response",
			reply:   agenttest.RPCReply{Result: json.RawMessage(`{"account":`)},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
				if request.Method == codexMethodAccountRead {
					return test.reply
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			a.threadID = "main-thread"
			err := a.PrepareInputProcess()
			if test.wantErr {
				require.Error(t, err)
				assert.NotErrorIs(t, err, agent.ErrInputProcessRestartRequired,
					"the failed check itself reports the failure; the mark drives the next attempt")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, test.wantMarked, recoveryRequired(a))
			if test.wantMarked {
				assert.ErrorContains(t, err, "approve recovery")
				assert.NotContains(t, err.Error(), "send the message again")
			}
			assert.Equal(t, test.wantChecked, inputChecked(a))
			require.Len(t, requests(), 1)
			assert.Equal(t, codexMethodAccountRead, requests()[0].Method)
			assert.Equal(t, false, requests()[0].Params["refreshToken"], "the check never forces token rotation")
		})
	}
}

// TestCodexPrepareInputProcessTranslatesWorkspaceRoutingUnauthorized feeds the
// account/read JSON-RPC rejection that workspace routing reports before any
// content turn exists.
func TestCodexPrepareInputProcessTranslatesWorkspaceRoutingUnauthorized(t *testing.T) {
	t.Parallel()
	a, _, requests := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
		if request.Method == codexMethodAccountRead {
			return agenttest.RPCReply{Error: json.RawMessage(
				`{"code":-32000,"message":"workspace routing discovery unauthorized (401)"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	require.Error(t, a.PrepareInputProcess())
	assert.True(t, recoveryRequired(a), "the rejection marks the process for the next input")
	require.Len(t, requests(), 1)
	// The next input asks for a replacement rather than repeating the read.
	err := a.PrepareInputProcess()
	require.Error(t, err)
	assert.ErrorIs(t, err, agent.ErrInputProcessRestartRequired)
	assert.Len(t, requests(), 1, "the marked process answers from its flag; no second account/read")
}

// TestCodexPrepareInputProcessTransientFailureRetriesWithoutMarking covers a
// transport-shaped account/read failure that recovers on the next attempt.
func TestCodexPrepareInputProcessTransientFailureRetriesWithoutMarking(t *testing.T) {
	t.Parallel()
	var reads int
	a, _, _ := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
		if request.Method == codexMethodAccountRead {
			reads++
			if reads == 1 {
				return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"temporarily unavailable"}`)}
			}
			return accountReadReply("account-1", true)
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	require.Error(t, a.PrepareInputProcess())
	assert.False(t, recoveryRequired(a), "an unrecognized rejection marks nothing")
	assert.False(t, inputChecked(a))
	require.NoError(t, a.PrepareInputProcess())
	assert.True(t, inputChecked(a), "a later successful check records the one-shot flag")
	assert.False(t, recoveryRequired(a))
}

// TestCodexPrepareInputProcessTimeoutStaysVisible uses a mock clock: an
// account/read that never answers fails through the API timeout, marks
// nothing, and accepts no input.
func TestCodexPrepareInputProcessTimeoutStaysVisible(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	clock := testutil.NewQuartzMock(t)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, providerkit.AwaitResponseTimerTag)
	a, _, requests := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, deliver func(agenttest.RPCReply)) {
		if request.Method == codexMethodAccountRead {
			return // Hold the answer until the timer fires.
		}
		deliver(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	}, clock)
	a.threadID = "main-thread"

	done := make(chan error, 1)
	go func() { done <- a.PrepareInputProcess() }()
	require.Equal(t, a.APITimeout(), testutil.WaitForTimer(t, ctx, newTimer))
	// The timer arms after the request write returns, but the fixture's
	// reader goroutine still has to record the line. Wait for the record, so
	// the final request-count assertion races nothing.
	require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, time.Millisecond)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, a.APITimeout(), stopTimer)

	select {
	case err := <-done:
		require.Error(t, err)
		assert.NotErrorIs(t, err, agent.ErrInputProcessRestartRequired)
	case <-ctx.Done():
		t.Fatal("the held account/read did not resolve at its timeout")
	}
	assert.False(t, recoveryRequired(a))
	assert.False(t, inputChecked(a), "a timeout is not a successful check")
	require.Len(t, requests(), 1)
}

// TestCodexPrepareInputProcessReportsMarkedProcessAtOnce verifies the retained
// flag short-circuits: no account/read, restart required.
func TestCodexPrepareInputProcessReportsMarkedProcessAtOnce(t *testing.T) {
	t.Parallel()
	a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	a.markAuthenticationRecoveryRequired()
	err := a.PrepareInputProcess()
	require.Error(t, err)
	assert.ErrorIs(t, err, agent.ErrInputProcessRestartRequired)
	assert.Empty(t, requests(), "the flag answers; the stale process receives nothing")
}

// TestCodexPrepareInputProcessSkipsAfterFirstSuccess feeds a successful check
// and asserts the second preparation sends no second account/read.
func TestCodexPrepareInputProcessSkipsAfterFirstSuccess(t *testing.T) {
	t.Parallel()
	a, _, requests := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
		if request.Method == codexMethodAccountRead {
			return accountReadReply("account-1", true)
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	require.NoError(t, a.PrepareInputProcess())
	require.NoError(t, a.PrepareInputProcess())
	assert.Len(t, requests(), 1)
}

// TestCodexAuthenticationRejectionIgnoresUncertainErrors pins the classifier's
// error half: only a decoded JSON-RPC rejection qualifies.
func TestCodexAuthenticationRejectionIgnoresUncertainErrors(t *testing.T) {
	t.Parallel()
	rejection := &providerkit.JSONRPCResponseError{Code: -32000, Message: "workspace routing discovery unauthorized (401)"}
	assert.True(t, codexAuthenticationRejection(rejection))
	assert.False(t, codexAuthenticationRejection(errors.New("write request: broken pipe")),
		"a transport failure carries no rejection to classify")
	assert.False(t, codexAuthenticationRejection(nil))
}
