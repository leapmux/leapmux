package validate

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SessionIDByteLimit is the maximum size of an agent session ID, in UTF-8
// bytes.
//
// The number matches NameByteLimit today, and the two are NOT linked on
// purpose. A session ID is a token, a name is text a user reads, and a change
// to what one may hold must not move the other.
const SessionIDByteLimit = 128

// sessionIDInvisible holds the invisible characters that a session ID may not
// hold. The list repeats invisibleFormat, and the repetition is the point:
// this rule is FROZEN, so a later change to what a NAME may hold cannot widen
// or narrow what a TOKEN may hold. TestSessionIDInvisibleMatchesNameRule
// reports the day the two lists stop agreeing, and a human then decides which
// one moves.
//
// A token carries no visible text, so every one of these can only travel
// through a copy and a paste unseen. A session ID that carries one names no
// session that the agent knows, and the resume then starts a new conversation
// with no report of why.
var sessionIDInvisible = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x00AD, Hi: 0x00AD, Stride: 1}, // SOFT HYPHEN
		{Lo: 0x061C, Hi: 0x061C, Stride: 1}, // ARABIC LETTER MARK
		{Lo: 0x180E, Hi: 0x180E, Stride: 1}, // MONGOLIAN VOWEL SEPARATOR
		{Lo: 0x200B, Hi: 0x200B, Stride: 1}, // ZERO WIDTH SPACE
		{Lo: 0x200E, Hi: 0x200F, Stride: 1}, // LEFT-TO-RIGHT MARK, RIGHT-TO-LEFT MARK
		// LEFT-TO-RIGHT EMBEDDING, RIGHT-TO-LEFT EMBEDDING, POP DIRECTIONAL
		// FORMATTING, LEFT-TO-RIGHT OVERRIDE, RIGHT-TO-LEFT OVERRIDE
		{Lo: 0x202A, Hi: 0x202E, Stride: 1},
		{Lo: 0x2060, Hi: 0x2060, Stride: 1}, // WORD JOINER
		// LEFT-TO-RIGHT ISOLATE, RIGHT-TO-LEFT ISOLATE, FIRST STRONG ISOLATE,
		// POP DIRECTIONAL ISOLATE
		{Lo: 0x2066, Hi: 0x2069, Stride: 1},
		{Lo: 0xFEFF, Hi: 0xFEFF, Stride: 1}, // ZERO WIDTH NO-BREAK SPACE
	},
	LatinOffset: 1,
}

// ValidateSessionID validates a session ID for resuming an agent session.
// It accepts the empty value, which means "no resume".
//
// This rule REFUSES rather than strips, and it owns its own character class
// rather than borrowing the name rule. A session ID is an opaque token that
// the worker hands to the agent: it becomes an argv element of
// `claude --resume <id>` and the `sessionId` member of an ACP request. A
// rewritten token resumes a different session, or no session, so the only
// correct answer for a character this rule does not want is to report it.
//
// The class is: valid UTF-8, no control character, no invisible format
// character, none of `"`, `\`, `$`, `%`, no whitespace at either end, no
// leading hyphen, non-empty, at most SessionIDByteLimit bytes.
//
// The browser copy is `validateSessionId` in `frontend/src/lib/validate.ts`,
// and `testdata/session_id_conformance.json` pins the two against each other.
// The fixture holds the ORDER of the tests as well as the class, because the
// two languages disagree about which characters a trim removes: Go's
// strings.TrimSpace claims U+0085 and JavaScript's String.prototype.trim
// claims U+FEFF. Both sides therefore run the character test BEFORE the
// whitespace-at-the-edge test, so the two report the same refusal for the
// same input.
func ValidateSessionID(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if len(sessionID) > SessionIDByteLimit {
		return fmt.Errorf("session ID must be at most %d bytes", SessionIDByteLimit)
	}
	// The UTF-8 test comes first, because every test below decodes. `for
	// range` yields U+FFFD for an invalid byte, and U+FFFD is not a control
	// character, not invisible, and none of the four punctuation marks -- so
	// without this line an invalid byte reaches `claude --resume` unreported.
	if !utf8.ValidString(sessionID) {
		return fmt.Errorf("session ID must be valid UTF-8")
	}
	for _, r := range sessionID {
		if unicode.IsControl(r) || unicode.Is(sessionIDInvisible, r) ||
			r == '"' || r == '\\' || r == '$' || r == '%' {
			return fmt.Errorf("session ID contains invalid characters")
		}
	}
	// The edge test reads the PINNED whitespace table, not strings.TrimSpace.
	//
	// TrimSpace reads Go's White_Space table and the browser's `trim` reads
	// the JavaScript engine's, and the two move on their own release
	// schedules. Here that is worse than the message divergence it caused for
	// U+0085 and U+FEFF, which the character test above now settles: a
	// Space_Separator that one runtime claims first makes one side ACCEPT a
	// token the other REFUSES, so the browser offers a resume that the worker
	// then rejects.
	if first, _ := utf8.DecodeRuneInString(sessionID); IsNameWhitespace(first) {
		return fmt.Errorf("session ID must not start or end with whitespace")
	}
	if last, _ := utf8.DecodeLastRuneInString(sessionID); IsNameWhitespace(last) {
		return fmt.Errorf("session ID must not start or end with whitespace")
	}
	// A token that STARTS with a hyphen is refused, because argv cannot tell it
	// from a flag. `claude --resume <id>` passes the token as its own argv
	// element, and `--resume` takes an OPTIONAL value, so a parser of that
	// shape does not read a hyphen-prefixed token as the value: it leaves
	// `--resume` empty and parses the token as a flag of its own. One argv
	// element is enough to reach `--dangerously-skip-permissions`. Quoting
	// stops a shell from reading the token as syntax; it does nothing about a
	// token that the AGENT reads as syntax.
	//
	// Nothing legitimate is lost. Every provider issues an opaque identifier --
	// a UUID, a ULID, a thread ID, a file path -- and none of them starts with
	// a hyphen. A hyphen anywhere else is ordinary and stays accepted.
	//
	// This test runs LAST, so ` -abc` and `-abc ` both report the whitespace
	// rule. Either order is correct; the shared fixture pins THIS one, so the
	// two languages report one message for one input.
	if strings.HasPrefix(sessionID, "-") {
		return fmt.Errorf("session ID must not start with a hyphen")
	}
	return nil
}
