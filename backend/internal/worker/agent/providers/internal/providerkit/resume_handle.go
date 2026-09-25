package providerkit

import (
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/util/validate"
)

// SessionFileHandleIsPath reports whether a resume handle identifies a session
// FILE rather than a session id: a separator anywhere, or the `.jsonl` suffix.
//
// It copies the resolver of each CLI that takes both shapes on one flag. Pi's
// `pi --session` (`resolveSessionPath` in pi's main.ts) and Oh My Pi's
// `omp --resume` (`createSessionManager` in omp's main.ts) apply the same test:
// a value that holds `/` or `\`, or that ends in `.jsonl`, is a path, and
// anything else is a session id. The answers must stay identical, because this
// decides which rule validates a handle and the CLI decides which lookup
// consumes it. A value that one reads as a path and the other as an id is
// validated against a rule that does not describe what happens to it.
func SessionFileHandleIsPath(handle string) bool {
	return strings.ContainsAny(handle, `/\`) || strings.HasSuffix(handle, ".jsonl")
}

// ResolveSessionFileOrIDHandle checks a resume handle that is EITHER a session
// file path or a session id, and returns the value that must reach argv.
//
// Two shapes need two rules, and each rule refuses the other shape. A path is
// not a token: a Windows path holds `\`, which the token class bans, and a real
// session path -- an escaped copy of the working directory plus a timestamped
// file name -- runs past the 128-byte token cap. An id is not a path: it is
// relative by construction, so the path rule refuses it with "path must be
// absolute". SessionFileHandleIsPath picks the rule.
//
// A path is still a value a user pastes into a field, so it is not unchecked:
// `SanitizePath` answers the traversal, the reserved device name and the
// absolute-path questions that a path raises, and the byte cap is the token
// cap's counterpart for the longer shape. The empty handle means "no resume" and
// is accepted, exactly as the token rule accepts it.
//
// The PATH shape returns SanitizePath's result, not the handle. SanitizePath
// normalizes before it judges -- it drops control characters, trims edge
// whitespace, expands `~` and cleans the path -- so the string it approved and
// the string the user typed differ whenever any of those applied. Both CLIs open
// a session file without requiring that it exists, so sending the typed string
// started an EMPTY session at a file name that held a stray control character,
// and the user's conversation was simply gone. Returning the approved string
// removes the gap rather than restating the rule at the sink.
func ResolveSessionFileOrIDHandle(handle, homeDir string) (string, error) {
	if handle == "" {
		return "", nil
	}
	if !SessionFileHandleIsPath(handle) {
		if err := validate.ValidateSessionID(handle); err != nil {
			return "", err
		}
		return handle, nil
	}
	// Measured before SanitizePath, which expands `~` and can therefore only
	// make the value longer than what the user typed.
	if len(handle) > contracts.SessionFilePathByteLimit {
		return "", fmt.Errorf("session file path: must be at most %d bytes", contracts.SessionFilePathByteLimit)
	}
	// An invisible-format character survives SanitizePath -- U+200B is Cf, not a
	// control character -- so a path that carries one would reach the CLI and
	// open a different file. The token rule refuses the same class, and refusing
	// it here keeps one answer for both shapes of one field.
	if err := validate.RefuseInvisibleSessionChars(handle); err != nil {
		return "", fmt.Errorf("session file path: %w", err)
	}
	sanitized, err := validate.SanitizePath(handle, homeDir)
	if err != nil {
		return "", fmt.Errorf("session file path: %w", err)
	}
	return sanitized, nil
}
