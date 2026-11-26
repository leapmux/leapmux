// Package cli holds what every command of the leapmux binary shares: the error
// envelope and its exit codes, the output printer, the command builder, and
// the resolution of an abbreviated command name.
//
// A command never prints its own failure. It returns an error, and the root
// prints that error once, as text on stderr or as an envelope on stdout with
// --json. One function, FromError, decides the code of every error, so no
// call site can choose a code that disagrees with the code of the same cause
// elsewhere.
package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"connectrpc.com/connect/v2"
)

// Code identifies the cause of a failure. A script branches on it, so each
// value identifies a cause, never the step that failed.
type Code string

// The codes of the error envelope. ExitCode maps each one to the exit status
// of the process.
const (
	// CodeUsage is a command line that cannot run: an unknown command or
	// flag, a missing argument, or a value the CLI rejects before it sends
	// anything.
	CodeUsage Code = "usage"
	// CodeInvalidArgument is a request that the hub or the Worker rejected
	// as malformed.
	CodeInvalidArgument Code = "invalid_argument"
	// CodeNotLoggedIn is a call without a usable credential.
	CodeNotLoggedIn Code = "not_logged_in"
	// CodePermissionDenied is a call whose credential does not allow it.
	CodePermissionDenied Code = "permission_denied"
	// CodeNotFound is a target that does not exist, or that the caller
	// cannot see.
	CodeNotFound Code = "not_found"
	// CodeAmbiguousTarget is a target that matches more than one object.
	// The envelope lists the candidates.
	CodeAmbiguousTarget Code = "ambiguous_target"
	// CodeConflict is a request that the current state refuses: a stale
	// version, an object that already exists, or a precondition that fails.
	CodeConflict Code = "conflict"
	// CodeSelfTargetRefused is a destructive command that would act on the
	// caller's own tab, tile or workspace. --allow-self overrides it.
	CodeSelfTargetRefused Code = "self_target_refused"
	// CodeTabBusyRefused is a close of a tab whose agent turn is in
	// progress. --allow-busy overrides it.
	CodeTabBusyRefused Code = "tab_busy_refused"
	// CodeAgentWaiting is a message to an agent that waits for an answer to
	// a permission prompt.
	CodeAgentWaiting Code = "agent_waiting"
	// CodeTimeout is a wait that ended before its condition held.
	CodeTimeout Code = "timeout"
	// CodeWorkerOffline is a Worker that the hub cannot reach now.
	CodeWorkerOffline Code = "worker_offline"
	// CodeUnavailable is a hub or a Worker that cannot answer now. A retry
	// can succeed.
	CodeUnavailable Code = "unavailable"
	// CodeVersionMismatch is a hub or a Worker that does not implement the
	// call, because its version differs from the CLI's.
	CodeVersionMismatch Code = "version_mismatch"
	// CodeInternal is every other failure.
	CodeInternal Code = "internal"
)

// The exit statuses of the process. Each code of the envelope maps to one of
// them, so a script can branch on the status without parsing the envelope.
const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitUsage       = 2
	ExitAuth        = 3
	ExitNotFound    = 4
	ExitRefused     = 5
	ExitWaitEnded   = 6
	ExitUnavailable = 7
	// ExitStopped reports a local daemon that is stopped (`daemon <mode> status`),
	// kept distinct from ExitAuth (3) so scripts can tell them apart binary-wide.
	ExitStopped = 8
)

// ExitCode returns the exit status for code.
func ExitCode(code Code) int {
	switch code {
	case CodeUsage:
		return ExitUsage
	case CodeNotLoggedIn, CodePermissionDenied:
		return ExitAuth
	case CodeNotFound, CodeAmbiguousTarget:
		return ExitNotFound
	case CodeSelfTargetRefused, CodeTabBusyRefused, CodeAgentWaiting:
		return ExitRefused
	case CodeTimeout:
		return ExitWaitEnded
	case CodeWorkerOffline, CodeUnavailable:
		return ExitUnavailable
	default:
		return ExitFailure
	}
}

// Candidate is one object that an ambiguous target matched.
type Candidate struct {
	ID    string `json:"id" table:"ID"`
	Kind  string `json:"kind,omitempty" table:"KIND"`
	Title string `json:"title,omitempty" table:"TITLE"`
}

// RetryReceipt provides idempotent retry metadata when a mutating command fails
// or times out with an uncertain or in-progress disposition.
type RetryReceipt struct {
	RequestID    string `json:"request_id"`
	Disposition  string `json:"disposition"`
	QueryCommand string `json:"query_command,omitempty"`
	RetryCommand string `json:"retry_command,omitempty"`
}

// Error is a failure that the root prints as the error envelope.
type Error struct {
	Code    Code
	Message string
	// Hints are the next steps that can resolve the failure, one sentence
	// each.
	Hints []string
	// Candidates lists the objects an ambiguous target matched.
	Candidates []Candidate
	// Receipt carries retry disposition and safe recovery commands when set.
	Receipt *RetryReceipt
	// exitCode, when not zero, replaces the status that Code maps to. Only a
	// wait sets it: a wait that ended without its condition is not a timeout
	// when the condition became impossible.
	exitCode int
	cause    error
}

// Error returns "<code>: <message>".
func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Unwrap returns the cause, so errors.Is can still match it.
func (e *Error) Unwrap() error { return e.cause }

// Retryable reports whether the same call can succeed later without a
// change.
func (e *Error) Retryable() bool {
	return e.Code == CodeUnavailable || e.Code == CodeWorkerOffline
}

// ExitCode returns the exit status of the process for this error.
func (e *Error) ExitCode() int {
	if e.exitCode != 0 {
		return e.exitCode
	}
	return ExitCode(e.Code)
}

// WithHint returns e with one more hint. It modifies e and returns it, so a
// constructor and its hints read as one expression.
func (e *Error) WithHint(format string, args ...any) *Error {
	e.Hints = append(e.Hints, fmt.Sprintf(format, args...))
	return e
}

// WithExitCode returns e with an exit status that replaces the one its code
// maps to.
func (e *Error) WithExitCode(code int) *Error {
	e.exitCode = code
	return e
}

// WithReceipt attaches retry disposition and recovery commands to e.
func (e *Error) WithReceipt(r *RetryReceipt) *Error {
	e.Receipt = r
	return e
}

// Newf builds an error with code and a formatted message.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Usagef builds a usage error. Return it for a command line that cannot run,
// before the command sends anything.
func Usagef(format string, args ...any) *Error {
	return Newf(CodeUsage, format, args...)
}

// Wrap builds an error with code whose message is msg followed by the
// message of cause. errors.Is still matches the cause.
func Wrap(code Code, cause error, msg string) *Error {
	if cause == nil {
		return &Error{Code: code, Message: msg}
	}
	text := cause.Error()
	if msg != "" {
		text = msg + ": " + text
	}
	return &Error{Code: code, Message: text, cause: cause}
}

// Classifier is an error that knows its own envelope. A package whose errors
// carry more than a Connect code implements it, so FromError does not have to
// import that package.
type Classifier interface {
	CLIError() *Error
}

// HintProvider attaches hints to an error chain without taking over its
// classification or discarding outer wrapped context.
type HintProvider interface {
	CLIHints() []string
}

// ErrNotLoggedIn marks a failure to load a credential. The control client
// wraps its own sentinel with it, so FromError can classify that failure
// without importing the client.
var ErrNotLoggedIn = errors.New("not logged in")

// FromError returns the envelope for err. It is the one place that maps a
// cause to a code:
//
//   - An *Error keeps its code.
//   - A Classifier states its own envelope.
//   - A Connect error maps its code (see codeOfConnect).
//   - A context deadline is an unavailable peer, and a canceled context is an
//     internal failure, because the CLI cancels only on its own shutdown.
//   - A network failure is an unavailable peer: a dial, a read or a write
//     that the network refused, and a network timeout.
//   - Anything else is internal.
func FromError(err error) *Error {
	if err == nil {
		return nil
	}
	out := classifyError(err)
	return attachProvidedHints(out, err)
}

func classifyError(err error) *Error {
	if e := extractEnvelope(err); e != nil {
		return e
	}
	if errors.Is(err, ErrNotLoggedIn) {
		return Wrap(CodeNotLoggedIn, err, "").
			WithHint(`run "leapmux auth login --hub <url>", or run the command inside a LeapMux tab`)
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return fromConnect(err, connectErr)
	}
	var opErr *net.OpError
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return Wrap(CodeUnavailable, err, "the call timed out")
	case errors.Is(err, context.Canceled):
		return Wrap(CodeInternal, err, "the call was canceled")
	case errors.As(err, &opErr):
		// A *url.Error is a net.Error too, but it also wraps a request that
		// never reached the network, such as an unsupported scheme. Only
		// the operation of the network itself says that the peer is
		// unreachable.
		return Wrap(CodeUnavailable, err, "")
	case errors.As(err, &netErr) && netErr.Timeout():
		return Wrap(CodeUnavailable, err, "the call timed out")
	}
	return Wrap(CodeInternal, err, "")
}

func attachProvidedHints(e *Error, err error) *Error {
	hints := collectProvidedHints(err)
	if len(hints) == 0 {
		return e
	}
	clone := *e
	clone.Hints = append([]string(nil), e.Hints...)
	out := &clone
	for _, hint := range hints {
		out = out.WithHint("%s", hint)
	}
	return out
}

func collectProvidedHints(err error) []string {
	if err == nil {
		return nil
	}
	var out []string
	if hp, ok := err.(HintProvider); ok {
		out = append(out, hp.CLIHints()...)
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		out = append(out, collectProvidedHints(u.Unwrap())...)
	case interface{ Unwrap() []error }:
		for _, child := range u.Unwrap() {
			out = append(out, collectProvidedHints(child)...)
		}
	}
	return out
}

// extractEnvelope walks err from outermost to innermost in the same order as
// errors.As, returning the first *Error or non-nil Classifier envelope.
func extractEnvelope(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	if c, ok := err.(Classifier); ok {
		if classified := c.CLIError(); classified != nil {
			return classified
		}
	}
	if as, ok := err.(interface{ As(any) bool }); ok {
		var e *Error
		if as.As(&e) && e != nil {
			return e
		}
		var c Classifier
		if as.As(&c) && c != nil {
			if classified := c.CLIError(); classified != nil {
				return classified
			}
		}
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return extractEnvelope(u.Unwrap())
	case interface{ Unwrap() []error }:
		for _, child := range u.Unwrap() {
			if e := extractEnvelope(child); e != nil {
				return e
			}
		}
	}
	return nil
}

// fromConnect maps a Connect error to its envelope. err is the whole chain,
// so the message keeps the context that callers wrapped around the Connect
// error.
//
// It adds no hint. The next step depends on the command: a refused
// credential asks for `leapmux auth login`, and a refused registration key
// of `leapmux daemon worker register` asks for a new key. The tree of each
// command adds its own.
func fromConnect(err error, connectErr *connect.Error) *Error {
	// A Connect error prints its own code before its message. The envelope
	// states a code of its own, so the message keeps the peer's words alone.
	message := err.Error()
	if connectErr.Message() != "" {
		message = strings.Replace(message, connectErr.Error(), connectErr.Message(), 1)
	}
	return &Error{Code: codeOfConnect(connectErr.Code()), Message: strings.TrimSpace(message), cause: err}
}

// codeOfConnect maps a Connect code to the code of the envelope.
func codeOfConnect(code connect.Code) Code {
	switch code {
	case connect.CodeNotFound:
		return CodeNotFound
	case connect.CodePermissionDenied:
		return CodePermissionDenied
	case connect.CodeUnauthenticated:
		return CodeNotLoggedIn
	case connect.CodeInvalidArgument, connect.CodeOutOfRange:
		return CodeInvalidArgument
	case connect.CodeAlreadyExists, connect.CodeAborted, connect.CodeFailedPrecondition:
		return CodeConflict
	case connect.CodeUnavailable, connect.CodeResourceExhausted, connect.CodeDeadlineExceeded:
		return CodeUnavailable
	case connect.CodeUnimplemented:
		return CodeVersionMismatch
	default:
		return CodeInternal
	}
}
