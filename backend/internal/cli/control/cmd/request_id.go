package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/id"
)

// --request-id makes a retry of `agent send` and `agent open` safe. The
// Worker already takes a repeated request as the first one when the request
// carries the same identity: EnqueueAgentInput by its input id, and OpenAgent
// by the id of the new tab. The CLI derives those ids from the request id,
// so a second run of the same command line carries the same ids and changes
// nothing, and a run with other content conflicts instead of replacing the
// first.
//
// The CLI derives the ids instead of passing the request id itself, because
// a tab id has a format and a length that a caller's text need not meet, and
// a short tab id would read as a prefix to every later command. A derived id
// has the shape of every other id.

// requestIDFlag is the flag that a retryable command takes.
const requestIDFlag = "request-id"

// The scopes of a derived id. Each request derives each of its ids in its
// own scope, so two commands that a caller gives the same request id never
// derive the same id.
const (
	// requestScopeAgentSend derives the input id of `agent send`.
	requestScopeAgentSend = "agent send"
	// requestScopeAgentOpen derives the tab id of `agent open`.
	requestScopeAgentOpen = "agent open"
	// requestScopeAgentOpenMessage derives the input id of the first message
	// of `agent open --message`.
	requestScopeAgentOpenMessage = "agent open message"
)

// addRequestIDFlag registers --request-id.
func addRequestIDFlag(fs *pflag.FlagSet) {
	fs.String(requestIDFlag, "", "an id of your choice that makes a retry safe: a second run with the same id and the same arguments creates nothing new")
}

// requestIDOf returns --request-id, or "" when the command line omits it. A
// flag that the command line gives with no text fails, because an unset
// variable in a script would otherwise turn the retry protection off.
func requestIDOf(inv *cli.Invocation) (string, error) {
	if !inv.Changed(requestIDFlag) {
		return "", nil
	}
	value := stringFlag(inv, requestIDFlag)
	if strings.TrimSpace(value) == "" {
		return "", cli.Usagef("--%s is empty", requestIDFlag)
	}
	return value, nil
}

// withRequestIDHint adds the cause that a request id gives to a conflict: a
// request id that an earlier request with other arguments already took. It
// returns err as it is without a request id, and for any other failure.
func withRequestIDHint(err error, requestID string) error {
	if err == nil || requestID == "" {
		return err
	}
	e := cli.FromError(err)
	if e.Code != cli.CodeConflict {
		return err
	}
	return e.WithHint("a retry repeats the command line of the first run; when --request-id %q belongs to a request with other arguments, give this request a new id", requestID)
}

// requestScopedID derives the id of one object of a request: the request
// id, the scope, and the parts that identify the target, such as the agent
// or the Worker. Each part carries its length, so no two lists of parts give
// the same input to the hash: no separator is safe, because a request id is
// the caller's own text.
//
// Without a request id, it returns a fresh random id.
func requestScopedID(requestID, scope string, parts ...string) string {
	if requestID == "" {
		return id.Generate()
	}
	var b strings.Builder
	b.WriteString("leapmux control request id")
	for _, part := range append([]string{scope, requestID}, parts...) {
		b.WriteString(":")
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteString(":")
		b.WriteString(part)
	}
	sum := sha256.Sum256([]byte(b.String()))
	// Hex keeps the id inside the letters and digits that every id uses, and
	// the length of a generated id keeps it a full id to the resolver.
	return hex.EncodeToString(sum[:])[:id.GeneratedLength]
}
