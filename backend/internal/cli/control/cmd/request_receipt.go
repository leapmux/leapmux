package cmd

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/shellquote"
)

// retryStage identifies where in a mutating command's lifecycle a failure
// happened, so the CLI can classify whether the server may have applied it.
type retryStage int

const (
	retryStageBeforeSend retryStage = iota
	retryStageDuringSend
	retryStageDuringWait
)

const (
	dispositionNotSent        = "not_sent"
	dispositionInProgress     = "in_progress"
	dispositionOutcomeUnknown = "outcome_unknown"
)

type retryReceipt struct {
	RequestID      string
	Explicit       bool
	CreatedAgentID string
	QueryCommand   string
	RetryCommand   string
}

// ensureRequestID returns the caller's trimmed --request-id when provided, or
// generates a fresh ID when omitted so every retryable command carries a
// durable receipt. An explicit flag with no non-whitespace text fails, because
// an unset variable in a script would otherwise turn retry protection off. The
// second return value reports whether the caller passed --request-id explicitly.
func ensureRequestID(inv *cli.Invocation) (requestID string, explicit bool, err error) {
	if !inv.Changed(requestIDFlag) {
		return id.Generate(), false, nil
	}
	value := strings.TrimSpace(stringFlag(inv, requestIDFlag))
	if value == "" {
		return "", true, cli.Usagef("--%s is empty", requestIDFlag)
	}
	return value, true, nil
}

// classifyRetryDisposition maps (error, stage) to one of Orca-style recovery
// dispositions: not_sent, in_progress, or outcome_unknown.
func classifyRetryDisposition(err error, stage retryStage) string {
	return classifyRetryDispositionForCode(cli.FromError(err).Code, stage)
}

func classifyRetryDispositionForCode(code cli.Code, stage retryStage) string {
	switch stage {
	case retryStageBeforeSend:
		return dispositionNotSent
	case retryStageDuringWait:
		return dispositionInProgress
	case retryStageDuringSend:
		switch code {
		case cli.CodeUsage, cli.CodeInvalidArgument, cli.CodeNotLoggedIn, cli.CodePermissionDenied,
			cli.CodeNotFound, cli.CodeAmbiguousTarget, cli.CodeSelfTargetRefused, cli.CodeTabBusyRefused,
			cli.CodeAgentWaiting, cli.CodeVersionMismatch:
			return dispositionNotSent
		default:
			return dispositionOutcomeUnknown
		}
	default:
		return dispositionOutcomeUnknown
	}
}

// retryReceiptError attaches a cli.RetryReceipt and recovery hints without
// eagerly converting the wrapped error into a *cli.Error, so callers still see
// the underlying Connect error and cli.HintProvider hints are not attached
// twice.
type retryReceiptError struct {
	err     error
	receipt *cli.RetryReceipt
	hints   []string
}

var (
	_ cli.Classifier   = (*retryReceiptError)(nil)
	_ cli.HintProvider = (*retryReceiptError)(nil)
)

func (e *retryReceiptError) Error() string      { return e.err.Error() }
func (e *retryReceiptError) Unwrap() error      { return e.err }
func (e *retryReceiptError) CLIHints() []string { return e.hints }

func (e *retryReceiptError) CLIError() *cli.Error {
	base := cli.FromError(e.err)
	// cli.FromError(e.err) already ran attachProvidedHints on e.err, and the
	// outer cli.FromError(e) call runs attachProvidedHints on e (which unwraps
	// to e.err) right after CLIError returns. Strip the trailing HintProvider
	// hints that cli.FromError(e.err) appended so each hint prints once.
	innerHints := collectProvidedHints(e.err)
	clone := *base
	if n := len(innerHints); n > 0 && len(base.Hints) >= n && slices.Equal(base.Hints[len(base.Hints)-n:], innerHints) {
		clone.Hints = append([]string(nil), base.Hints[:len(base.Hints)-n]...)
	} else {
		clone.Hints = append([]string(nil), base.Hints...)
	}
	clone.Receipt = e.receipt
	return &clone
}

func collectProvidedHints(err error) []string {
	if err == nil {
		return nil
	}
	var out []string
	if hp, ok := err.(cli.HintProvider); ok {
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

// wrapRetryReceipt attaches a structured cli.RetryReceipt and actionable hints
// to err when requestID is present. Conflict errors during send keep their
// dedicated request-id conflict hint; ExitStatus values pass through unwrapped.
func wrapRetryReceipt(err error, stage retryStage, r retryReceipt) error {
	if err == nil || r.RequestID == "" {
		return err
	}
	var exitStatus *cli.ExitStatus
	if errors.As(err, &exitStatus) {
		return err
	}
	code := cli.FromError(err).Code
	if code == cli.CodeConflict && stage == retryStageDuringSend {
		if r.Explicit {
			return withRequestIDHint(err, r.RequestID)
		}
		if r.CreatedAgentID == "" {
			return err
		}
	}
	disp := classifyRetryDispositionForCode(code, stage)
	if r.CreatedAgentID != "" && disp == dispositionNotSent {
		// An earlier step already created the agent tab, so the overall command
		// has server-side effects even when the follow-up input was not sent.
		disp = dispositionInProgress
	}
	rec := &cli.RetryReceipt{
		RequestID:    r.RequestID,
		Disposition:  disp,
		QueryCommand: r.QueryCommand,
		RetryCommand: r.RetryCommand,
	}
	hints := []string{fmt.Sprintf("disposition: %s (request_id: %s)", disp, r.RequestID)}
	if r.CreatedAgentID != "" {
		hints = append(hints, fmt.Sprintf("agent %s was already created", r.CreatedAgentID))
	}
	if r.QueryCommand != "" {
		hints = append(hints, fmt.Sprintf("check status: %s", r.QueryCommand))
	}
	if r.RetryCommand != "" && disp != dispositionNotSent {
		hints = append(hints, fmt.Sprintf("safe retry: %s", r.RetryCommand))
	}
	return &retryReceiptError{
		err:     err,
		receipt: rec,
		hints:   hints,
	}
}

func buildAgentRequestStatusCmd(agentID, requestID string) string {
	return fmt.Sprintf("leapmux agent request-status %s %s", shellquote.POSIX.Arg(agentID), shellquote.POSIX.Arg(requestID))
}

func buildAgentSendRetryCmd(inv *cli.Invocation, agentID, text string, wait bool, requestID string) string {
	parts := []string{"leapmux", "agent", "send", shellquote.POSIX.Arg(agentID)}
	if inv != nil {
		for _, path := range inv.StringArray("attach") {
			parts = append(parts, "--attach", shellquote.POSIX.Arg(path))
		}
		if inv.Changed("at") {
			parts = append(parts, "--at", shellquote.POSIX.Arg(stringFlag(inv, "at")))
		}
		if boolFlag(inv, "steer") {
			parts = append(parts, "--steer")
		}
		if boolFlag(inv, "preempt") {
			parts = append(parts, "--preempt")
		}
		if boolFlag(inv, "all") {
			parts = append(parts, "--all")
		}
		if inv.Changed("timeout") {
			parts = append(parts, "--timeout", shellquote.POSIX.Arg(inv.Command.Flags().Lookup("timeout").Value.String()))
		}
		if inv.Changed(outputSchemaFlag) {
			parts = append(parts, "--"+outputSchemaFlag, shellquote.POSIX.Arg(stringFlag(inv, outputSchemaFlag)))
		}
		if inv.Changed(schemaRetriesFlag) {
			parts = append(parts, "--"+schemaRetriesFlag, shellquote.POSIX.Arg(inv.Command.Flags().Lookup(schemaRetriesFlag).Value.String()))
		}
	}
	if wait {
		parts = append(parts, "--wait")
	}
	parts = append(parts, "--request-id", shellquote.POSIX.Arg(requestID))
	if text != "" {
		if strings.HasPrefix(text, "-") {
			parts = append(parts, "--")
		}
		parts = append(parts, shellquote.POSIX.Arg(text))
	}
	return strings.Join(parts, " ")
}

func appendSharedOpenRetryFlags(inv *cli.Invocation, parts []string, stringFlags []string) []string {
	for _, flagName := range stringFlags {
		if inv.Changed(flagName) {
			parts = append(parts, "--"+flagName, shellquote.POSIX.Arg(stringFlag(inv, flagName)))
		}
	}
	for _, opt := range inv.StringArray("option") {
		parts = append(parts, "--option", shellquote.POSIX.Arg(opt))
	}
	for _, path := range inv.StringArray("attach") {
		parts = append(parts, "--attach", shellquote.POSIX.Arg(path))
	}
	for _, boolName := range []string{"first", "last", "focus", "no-focus", "wait", "all"} {
		if inv.Changed(boolName) && boolFlag(inv, boolName) {
			parts = append(parts, "--"+boolName)
		}
	}
	if inv.Changed("timeout") {
		parts = append(parts, "--timeout", shellquote.POSIX.Arg(inv.Command.Flags().Lookup("timeout").Value.String()))
	}
	if inv.Changed(outputSchemaFlag) {
		parts = append(parts, "--"+outputSchemaFlag, shellquote.POSIX.Arg(stringFlag(inv, outputSchemaFlag)))
	}
	if inv.Changed(schemaRetriesFlag) {
		parts = append(parts, "--"+schemaRetriesFlag, shellquote.POSIX.Arg(inv.Command.Flags().Lookup(schemaRetriesFlag).Value.String()))
	}
	return parts
}

func buildAgentOpenRetryCmd(inv *cli.Invocation, requestID string) string {
	parts := []string{"leapmux", "agent", "open"}
	parts = appendSharedOpenRetryFlags(inv, parts, []string{
		"provider", "model", "effort", "permission-mode", "title", "name",
		"message", "file", "resume", "dir", "worktree", "use-worktree",
		"checkout", "new-branch", "base", "workspace", "tile", "worker",
		"before", "after",
	})
	parts = append(parts, "--request-id", shellquote.POSIX.Arg(requestID))
	if inv != nil && inv.HasArg("prompt") {
		if prompt := inv.Arg("prompt"); prompt != "" {
			if strings.HasPrefix(prompt, "-") {
				parts = append(parts, "--")
			}
			parts = append(parts, shellquote.POSIX.Arg(prompt))
		}
	}
	return strings.Join(parts, " ")
}

func buildAgentForkRetryCmd(inv *cli.Invocation, requestID string) string {
	parts := []string{"leapmux", "agent", "fork", shellquote.POSIX.Arg(inv.Arg("agent"))}
	parts = appendSharedOpenRetryFlags(inv, parts, []string{
		"model", "effort", "permission-mode", "title", "name",
		"message", "file", "dir", "worktree", "use-worktree",
		"checkout", "new-branch", "base", "workspace", "tile", "worker",
		"before", "after",
	})
	if inv != nil && inv.Changed("fresh") && boolFlag(inv, "fresh") {
		parts = append(parts, "--fresh")
	}
	parts = append(parts, "--request-id", shellquote.POSIX.Arg(requestID))
	if inv != nil && inv.HasArg("prompt") {
		if prompt := inv.Arg("prompt"); prompt != "" {
			if strings.HasPrefix(prompt, "-") {
				parts = append(parts, "--")
			}
			parts = append(parts, shellquote.POSIX.Arg(prompt))
		}
	}
	return strings.Join(parts, " ")
}

func buildTerminalRunRetryCmd(inv *cli.Invocation, terminalID, command string, wait bool, requestID string) string {
	parts := []string{"leapmux", "terminal", "run", shellquote.POSIX.Arg(terminalID)}
	if wait {
		parts = append(parts, "--wait")
	}
	if inv != nil {
		if boolFlag(inv, "exit") {
			parts = append(parts, "--exit")
		}
		if inv.Changed("text") {
			parts = append(parts, "--text", shellquote.POSIX.Arg(stringFlag(inv, "text")))
		}
		if inv.Changed("regex") {
			parts = append(parts, "--regex", shellquote.POSIX.Arg(stringFlag(inv, "regex")))
		}
		if boolFlag(inv, "change") {
			parts = append(parts, "--change")
		}
		if inv.Changed("after-offset") {
			parts = append(parts, "--after-offset", shellquote.POSIX.Arg(inv.Command.Flags().Lookup("after-offset").Value.String()))
		}
		if boolFlag(inv, "scrollback") {
			parts = append(parts, "--scrollback")
		}
		if inv.Changed("timeout") {
			parts = append(parts, "--timeout", shellquote.POSIX.Arg(inv.Command.Flags().Lookup("timeout").Value.String()))
		}
		if inv.Changed("workspace") {
			parts = append(parts, "--workspace", shellquote.POSIX.Arg(stringFlag(inv, "workspace")))
		}
	}
	parts = append(parts, "--request-id", shellquote.POSIX.Arg(requestID))
	if strings.HasPrefix(command, "-") {
		parts = append(parts, "--")
	}
	parts = append(parts, shellquote.POSIX.Arg(command))
	return strings.Join(parts, " ")
}
