package codex

// This file owns the process-local authentication recovery state: the
// classification of native Codex failures that mean this process can no longer
// accept input, the flag that retains that fact, and the input preparation
// that reports it to the Worker.
//
// Everything here is Codex's own wire knowledge, so it stays in this package
// (see the provider-boundary rule). The Worker learns only
// agent.ErrInputProcessRestartRequired.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codexMethodAccountRead is a Codex method that no contract table lists,
// because only Go reads it.
const codexMethodAccountRead = "account/read"

// codexRefreshTokenFailureClauses are the distinctive clauses of the four
// native refresh-token failure messages (codex-rs/login manager.rs): the
// refresh token expired, was already used, was revoked, or the stored login
// moved to another account. Codex states these in the error message of a
// failed turn, with no structured variant of its own, so the clause is the
// signal. Matched case-insensitively against the whole message, because Codex
// prefixes the clause with "Your access token could not be refreshed because".
var codexRefreshTokenFailureClauses = []string{
	"your refresh token has expired",
	"your refresh token was already used",
	"your refresh token was revoked",
	"you have since logged out or signed in to another account",
}

// codexRefreshTokenFailureCodes are the token-endpoint error codes that the
// same login module classifies (classify_refresh_token_failure). A model
// request the credential failed surfaces its 403 as the raw HTTP failure text
// with the endpoint's body inlined, so the CODE is the credential signal the
// message carries -- the observed shape, not a guess.
var codexRefreshTokenFailureCodes = []string{
	"refresh_token_expired",
	"refresh_token_invalidated",
	"refresh_token_reused",
}

// codexWorkspaceRoutingUnauthorizedClause identifies the one other native
// credential message: the workspace-routing discovery that account/read runs
// before any content turn exists reports its HTTP 401 as this JSON-RPC error
// text, with no structured codexErrorInfo at all (app-server
// workspace_routing.rs).
const codexWorkspaceRoutingUnauthorizedClause = "workspace routing discovery unauthorized"

// codexErrorInfoKind states which native codexErrorInfo shape a failure
// carried: the bare "unauthorized" variant, one of the variants that carry an
// HTTP status, or another variant this classifier does not recognize.
type codexErrorInfoKind int

const (
	codexErrorInfoNone codexErrorInfoKind = iota
	codexErrorInfoUnauthorized
	codexErrorInfoHTTPStatus
	codexErrorInfoOther
)

// codexErrorInfo is one decoded codexErrorInfo value. status is nil for an
// HTTP-status variant whose status the native error did not state.
type codexErrorInfo struct {
	kind   codexErrorInfoKind
	status *uint16
}

// codexErrorInfoName reads the bare string variant of one codexErrorInfo
// value, or "" for an absent, null, object, or non-string value.
func codexErrorInfoName(raw json.RawMessage) string {
	if providerkit.RawKindOf(raw) != providerkit.RawValue {
		return ""
	}
	var name string
	if json.Unmarshal(raw, &name) != nil {
		return ""
	}
	return name
}

// codexParsedErrorInfo decodes one codexErrorInfo value. ok is false for an
// absent or null value and for a value this classifier cannot read -- a shape
// that does not parse is no authentication signal, so the caller keeps the
// failure's existing handling instead of guessing.
func codexParsedErrorInfo(raw json.RawMessage) (codexErrorInfo, bool) {
	switch providerkit.RawKindOf(raw) {
	case providerkit.RawAbsent, providerkit.RawNull:
		return codexErrorInfo{}, false
	case providerkit.RawValue:
		// Decode the explicit value below.
	}
	if name := codexErrorInfoName(raw); name != "" {
		if name == "unauthorized" {
			return codexErrorInfo{kind: codexErrorInfoUnauthorized}, true
		}
		return codexErrorInfo{kind: codexErrorInfoOther}, true
	}
	// The app-server protocol serializes the enum's data-carrying variants as
	// one-member objects with camelCase names ({"httpConnectionFailed":
	// {"httpStatusCode": 403}}), the spelling the installed CLI emits.
	var object struct {
		HTTPConnectionFailed           *codexHTTPStatus `json:"httpConnectionFailed"`
		ResponseStreamConnectionFailed *codexHTTPStatus `json:"responseStreamConnectionFailed"`
		ResponseStreamDisconnected     *codexHTTPStatus `json:"responseStreamDisconnected"`
		ResponseTooManyFailedAttempts  *codexHTTPStatus `json:"responseTooManyFailedAttempts"`
	}
	if json.Unmarshal(raw, &object) != nil {
		return codexErrorInfo{}, false
	}
	info := codexErrorInfo{}
	for _, variant := range []*codexHTTPStatus{
		object.HTTPConnectionFailed,
		object.ResponseStreamConnectionFailed,
		object.ResponseStreamDisconnected,
		object.ResponseTooManyFailedAttempts,
	} {
		if variant != nil {
			info.kind, info.status = codexErrorInfoHTTPStatus, variant.HTTPStatusCode
			return info, true
		}
	}
	return codexErrorInfo{kind: codexErrorInfoOther}, true
}

// codexHTTPStatus mirrors the native variant payloads. A negative or oversized
// status fails the whole decode, which codexParsedErrorInfo reports as an
// unreadable shape rather than a signal.
type codexHTTPStatus struct {
	HTTPStatusCode *uint16 `json:"httpStatusCode"`
}

// codexRefreshTokenFailureMessage reports whether one native error message
// states a refresh-token failure that no retry of this process can repair:
// either the failure message itself, or the token-endpoint error code that the
// failing HTTP response inlined into it.
func codexRefreshTokenFailureMessage(message string) bool {
	lower := strings.ToLower(message)
	for _, clause := range codexRefreshTokenFailureClauses {
		if strings.Contains(lower, clause) {
			return true
		}
	}
	for _, code := range codexRefreshTokenFailureCodes {
		if strings.Contains(lower, code) {
			return true
		}
	}
	return false
}

// codexCredentialFailureMessage reports whether one native error message
// states a credential failure this process cannot repair: a refresh-token
// failure, or the workspace-routing discovery 401.
func codexCredentialFailureMessage(message string) bool {
	return codexRefreshTokenFailureMessage(message) ||
		strings.Contains(strings.ToLower(message), codexWorkspaceRoutingUnauthorizedClause)
}

// codexAuthenticationFailure reports whether one native Codex failure means
// this process can no longer authenticate: its stored login expired, was
// revoked, was reused, or moved to another account, or the server rejected its
// credential with HTTP 401.
//
// The cases that must NOT qualify: a bare HTTP 403 (an ordinary permission
// refusal), rate limits and server errors, a message that merely mentions
// authentication, an MCP server's OAuth failure, and a transient
// authentication-recovery notification. A 403 counts only when its message
// states one of the recognized refresh-token failures.
func codexAuthenticationFailure(message string, codexErrorInfoRaw json.RawMessage) bool {
	if codexCredentialFailureMessage(message) {
		return true
	}
	info, ok := codexParsedErrorInfo(codexErrorInfoRaw)
	if !ok {
		return false
	}
	switch info.kind {
	case codexErrorInfoUnauthorized:
		return true
	case codexErrorInfoHTTPStatus:
		if info.status == nil {
			return false
		}
		switch *info.status {
		case 401:
			return true
		case 403:
			return codexRefreshTokenFailureMessage(message)
		}
	case codexErrorInfoNone, codexErrorInfoOther:
		// No credential signal: an absent value and an unrecognized variant
		// keep the failure's existing handling.
	}
	return false
}

// codexAuthenticationRejection reports whether err is an explicitly rejected
// native request whose JSON-RPC error states an authentication failure. A
// timeout or a lost response carries no JSON-RPC error, so an uncertain
// delivery never qualifies: replay permission cannot come from a lost answer.
func codexAuthenticationRejection(err error) bool {
	var responseErr *providerkit.JSONRPCResponseError
	return errors.As(err, &responseErr) && codexAuthenticationFailure(responseErr.Message, responseErr.Data)
}

// markAuthenticationRecoveryRequired retains a confirmed authentication
// failure on this physical process. Later input then asks the Worker for a
// replacement instead of reaching this process again. Agent.Mu protects the
// flag; a replacement process starts with fresh state, because the flag
// belongs to the process, not the agent record.
func (a *Agent) markAuthenticationRecoveryRequired() {
	a.Mu.Lock()
	a.authenticationRecoveryRequired = true
	a.Mu.Unlock()
}

// The assertion makes a drift from the shared preparation seam a compile error.
var _ agent.InputProcessPreparer = (*Agent)(nil)

// PrepareInputProcess implements agent.InputProcessPreparer. It reports the
// retained recovery state first; otherwise it verifies the account state once,
// before this process's first input.
//
// It never holds Mu or outputMu while it waits for the native response: the
// output reader needs those locks to process the reply.
func (a *Agent) PrepareInputProcess() error {
	a.Mu.Lock()
	recoveryRequired := a.authenticationRecoveryRequired
	checked := a.inputAuthenticationChecked
	a.Mu.Unlock()
	if recoveryRequired {
		return fmt.Errorf("%w: the Codex login that this process holds cannot accept further input",
			agent.ErrInputProcessRestartRequired)
	}
	if checked {
		return nil
	}
	return a.checkInputAuthentication()
}

// checkInputAuthentication reads the account state once, before this
// process's first input. It establishes that Codex reports an account; it
// does not claim to prove model entitlement or that the server will accept a
// token.
//
// A successful check records inputAuthenticationChecked, so one process pays
// one account/read. A malformed response or a transient transport failure
// leaves the flag false, marks nothing, and stays a visible failure: the next
// input checks again rather than trusting an unreadable answer.
//
// Codex keeps its credential storage to itself: this method sends Codex's own
// account/read request and reads its answer. LeapMux reads no token, no
// auth.json, and no operating-system credential store, and forces no
// rotation.
func (a *Agent) checkInputAuthentication() error {
	params, err := json.Marshal(map[string]interface{}{"refreshToken": false})
	if err != nil {
		return fmt.Errorf("marshal account/read params: %w", err)
	}
	resp, err := a.SendRequest(codexMethodAccountRead, params, a.APITimeout())
	if err != nil {
		if codexAuthenticationRejection(err) {
			// A rejection the account/read RPC itself reported -- the
			// workspace-routing discovery unauthorized case reaches here before
			// any content turn exists. Mark the process, so a later input asks
			// for a replacement instead of repeating this read against the
			// stale process indefinitely.
			a.markAuthenticationRecoveryRequired()
		}
		return fmt.Errorf("codex account/read: %w", err)
	}
	var result struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenaiAuth *bool           `json:"requiresOpenaiAuth"`
	}
	if err := json.Unmarshal(resp, &result); err != nil || result.RequiresOpenaiAuth == nil {
		return fmt.Errorf("codex account/read: response %q stated no requiresOpenaiAuth", string(resp))
	}
	if *result.RequiresOpenaiAuth && providerkit.RawKindOf(result.Account) != providerkit.RawValue {
		// Codex requires a sign-in and reports no account: the user is signed
		// out on this machine. Mark the process, so the next input replaces it
		// once a fresh login exists, and fail this preparation: the input
		// stays in the queue for that retry.
		a.markAuthenticationRecoveryRequired()
		return errors.New("codex requires sign-in; log in and send the message again")
	}
	a.Mu.Lock()
	a.inputAuthenticationChecked = true
	a.Mu.Unlock()
	return nil
}
