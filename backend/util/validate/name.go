package validate

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
)

// NameByteLimit is the maximum size of a name or a title, in UTF-8 bytes.
//
// The limit counts UTF-8 bytes, because `len` counts bytes. 128 bytes holds
// 128 plain ASCII characters, but only approximately 42 CJK characters. The
// browser copy in `frontend/src/lib/validate.ts` counts the same bytes and
// states the same limit. A name that the field accepts is therefore a name
// that this package accepts.
const NameByteLimit = contracts.NameByteLimit

// cleanScanLimit is where CleanNameChars stops appending.
//
// One byte past the name limit is enough for its caller: SanitizeName only has
// to SEE that the result is longer than the limit to refuse it, and a result
// that stopped here is at least one byte longer. CleanNameTo derives the same
// stop from ITS byte limit rather than reading this constant, because the stop
// caps the result and a fixed one would cap every label at the name limit.
//
// The stop limits the OUTPUT, not the input. It caps the allocation at 129
// bytes for a title that no name rule can use, such as a whole log line that
// a provider reported as a "title". It does not cap the SCAN: an input that
// holds only whitespace, control characters, and invisible characters never
// grows the builder, so the loop reads every byte of it. The callers that
// accept such a title are bounded elsewhere -- the hub caps a request body at
// 4 MiB, and the worker caps one provider line at contracts.MaxMessageSize.
const cleanScanLimit = NameByteLimit + 1

// invisibleFormat holds the invisible characters that a name loses. The set
// is a CURATED SUBSET of the characters that occupy no width, and not every
// such character. A name rule strips a member for the reason it strips a
// control character: the reader cannot see it, so it can only hide text or
// pad a name past a limit that the visible characters fit.
//
// The table is written out by CODE POINT rather than tested with
// `unicode.Is(unicode.Cf, r)`, because the browser copy of this rule
// (`frontend/src/lib/validate.ts`) must strip the SAME set. Go and each
// JavaScript engine update their Unicode tables on their own release
// schedules, and a category test would let the two sides disagree on a
// character that one of them classifies first. The two lists are pinned
// against each other by testdata/title_cleaning_conformance.json.
//
// U+FEFF (the byte order mark) is one member and no longer a special case.
// This package gave it its own constant before, because it is the one input
// on which this rule and its browser copy disagreed. JavaScript's
// String.prototype.trim removes U+FEFF, and Go's strings.TrimSpace keeps it.
// A pasted title that carried a byte order mark therefore gave the browser
// "Plan", and gave this package a "Plan" that still holds the mark.
//
// Three groups stay deliberately, although they are also invisible:
//
//   - U+200C ZERO WIDTH NON-JOINER and U+200D ZERO WIDTH JOINER. The joiner
//     builds an emoji sequence (a family, a profession, a flag), and both
//     control the shape of a word in Indic, Persian, and Arabic orthography.
//     A strip rewrites the text.
//   - The variation selectors U+FE00-U+FE0F. U+FE0F is what makes a character
//     render as an emoji rather than as its text form.
//   - The tag characters U+E0020-U+E007F, which spell out a subdivision flag
//     such as the flag of Scotland. The table holds no R32 entry, so
//     `unicode.Is` reports false for every astral rune. That is the intent,
//     and TestCleanNameKeepsTagSequence pins it: an R32 entry added later, or
//     a switch to a `Cf` category test, breaks the flag of Scotland in every
//     tab title.
//
// Other invisible characters stay because nobody added them, and not because
// a name may hold them: the invisible math operators U+2061-U+2064, the
// interlinear annotation controls U+FFF9-U+FFFB, and the blank-glyph
// characters U+115F, U+1160, U+2800 and U+3164 all survive today. Add a code
// point to contracts/validate.json and a case to the shared fixture; both
// sides' tables regenerate from the contract, and the fixture proves the
// algorithms still agree.
var invisibleFormat = contracts.NameInvisibleFormat

// nameWhitespace holds the characters that a name rule folds to one space.
//
// It is written out by CODE POINT for the reason invisibleFormat is, and the
// reason applies with MORE force here. `unicode.IsSpace` reads Go's generated
// White_Space table, which moves with the Go release, and the browser copy
// reads `\s`, which moves with the JavaScript engine. The Cc category that
// `unicode.IsControl` reports is frozen, so the STRIP was already
// deterministic; the FOLD was the last half of this rule that two runtimes
// could answer differently. A Unicode release that adds a Space_Separator
// lands in the worker when the project bumps Go and in the browser when the
// user updates it -- different weeks -- so one side would fold a character
// that the other keeps, and the tab strip would show one title while the
// worker stored another with no error anywhere.
//
// The cost of pinning is that the set goes stale on purpose: a
// Space_Separator added to Unicode later renders as a visible character
// inside a title on BOTH sides until somebody adds it here. That failure is
// visible, and the drift it replaces was silent.
// TestNameWhitespaceMatchesUnicode is what reports the day the two disagree,
// so the staleness is a decision somebody makes rather than one that happens.
//
// ValidateSessionID reads this table as well. What a NAME may hold and what a
// TOKEN may hold are separate policies, and sessionid.go keeps its own frozen
// character class for that reason -- but "which characters are whitespace" is
// a fact about Unicode rather than a policy, so both rules read one answer.
var nameWhitespace = contracts.NameWhitespaceFold

// IsNameWhitespace reports whether a name rule folds r to one space.
//
// It reads the pinned table rather than `unicode.IsSpace`, so the answer
// cannot change under a Go upgrade without TestNameWhitespaceMatchesUnicode
// reporting it first.
func IsNameWhitespace(r rune) bool {
	return unicode.Is(nameWhitespace, r)
}

// IsUnreadable reports whether a reader cannot see r: r is a control
// character, or r is in invisibleFormat.
//
// It is the one predicate for "this character carries no visible text", and
// every rule in this repository that removes such a character asks it, so the
// set has one definition. CleanNameChars folds a whitespace control to a
// space instead, so it tests whitespace before it asks this.
func IsUnreadable(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(invisibleFormat, r)
}

// CleanNameChars applies the character rule that every name and title in
// LeapMux shares. It strips what a reader cannot see, folds each run of
// whitespace to one space, and trims both ends. It never fails.
//
// scanLimit stops the append once the result reaches that many bytes, so the
// result holds at most scanLimit+4 bytes: the check runs at the TOP of an
// iteration, and one iteration can still flush a pending space (1 byte) and
// then write a 4-byte rune. A scanLimit of zero or less scans the whole input,
// and the caller must then bound the INPUT itself.
//
// Call CleanName instead unless you need the character step WITHOUT the
// NameByteLimit cut. extractPlanTitle is the one caller that does: it strips
// a "Plan: " prefix from the cleaned text, so a cut applied BEFORE that strip
// spends part of the byte budget on a prefix that is about to go, and the
// title then loses as many bytes as the prefix held.
//
// It never GROWS the string, and the callers below depend on that:
//
//   - It strips a control character, an invisible format character, and an
//     invalid byte.
//   - A run of whitespace becomes one space (one byte), and every whitespace
//     character is at least one byte.
//   - It copies every other character unchanged.
//
// A name holds VISIBLE text, so the rule strips nothing else. `"`, `\`, `$`
// and `%` survive: no sink in this repository reads a stored name as syntax.
// The shell path quotes each argument (`posixQuote` in
// internal/worker/agent/shell.go), the stylesheet path escapes at the emitter
// (`buildFontFamily` in frontend/src/lib/fontStack.ts), the plan file name
// keeps letters and digits only (`SanitizePlanFilenameTitle`), and the SQL is
// parameterized. A guard at the emitter holds for whatever the store holds. A
// character ban here only removed the user's text, and it let each of those
// sinks stay wrong.
//
// The loop decodes by hand rather than with `for range`, because `for range`
// cannot report an invalid byte: it yields U+FFFD for one, which is 3 bytes
// out for 1 byte in. That was the one way this function could grow a string.
// A title of mostly invalid bytes was therefore discarded whole instead of
// cut, because the grown string failed the length check.
// `utf8.DecodeRuneInString` returns the size as well, so it tells an invalid
// byte (RuneError with size 1) apart from a U+FFFD that the caller sent (size
// 3), and strips the first one only.
func CleanNameChars(name string, scanLimit int) string {
	var b strings.Builder
	if scanLimit > 0 {
		b.Grow(min(len(name), scanLimit))
	} else {
		b.Grow(len(name))
	}

	// pendingSpace records that a whitespace run was seen and that the next
	// visible character needs one space before it. It stays false while the
	// builder is empty, which is what drops the whitespace at the START; it is
	// never flushed after the loop, which is what drops the whitespace at the
	// END. Together they do the work of a trim, without a second pass.
	pendingSpace := false

	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		i += size

		switch {
		case r == utf8.RuneError && size == 1:
			// An invalid byte. A real U+FFFD in the input decodes with size 3
			// and reaches the write below, so this case cannot strip one the
			// caller sent deliberately.
			continue
		case unicode.Is(invisibleFormat, r):
			// invisibleFormat is spelled out by code point so that this rule
			// and its browser copy cannot disagree. That holds only while the
			// table answers FIRST: `unicode.IsSpace` reads Go's White_Space
			// table, which moves with the Go release, so a member that a
			// future table claims as whitespace would fold here and strip in
			// the browser. No member is White_Space today, so this arm is
			// behavior-identical. It exists to keep the pinned set pinned.
			continue
		case unicode.Is(nameWhitespace, r):
			// The whitespace test runs BEFORE the control test, and that order
			// is required: the tab, the newline, the vertical tab, the form
			// feed, the carriage return and U+0085 are Cc AND whitespace at the
			// same time. A control test first stripped them, and
			// "Fix parser\nAdd tests" became "Fix parserAdd tests".
			//
			// It reads the pinned table and not `unicode.IsSpace`, so a Go
			// upgrade cannot move the fold set out from under the browser copy.
			pendingSpace = b.Len() > 0
			continue
		case unicode.IsControl(r):
			continue
		}

		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
		if scanLimit > 0 && b.Len() >= scanLimit {
			break
		}
	}

	return b.String()
}

// StripUnreadable removes from s every character that a reader cannot see --
// a control character, an invisible format character, and an invalid byte --
// and cuts the result to byteLimit UTF-8 bytes at a rune boundary. A
// byteLimit of zero or less applies no cut.
//
// It differs from CleanNameChars in the two ways that a NON-name value needs.
// It does not fold a whitespace run, because a key must keep the bytes that
// tell two keys apart. It does not trim, for the same reason. What stays is the
// part that every value shares: a reader cannot see the removed characters, so
// they can only hide text or reverse what the reader sees.
//
// WHITESPACE SURVIVES, INCLUDING A LINE BREAK. The whitespace test runs BEFORE
// the control test, the same order and for the same reason CleanNameChars uses
// it: \t, \n, \v, \f, \r and U+0085 are Cc AND whitespace at once, so a
// control test that ran first deleted them with nothing in their place and
// glued the words on either side together -- "Running tests\nfor the parser"
// became "Running testsfor the parser". A reader SEES a line break, so it is
// not what this helper exists to remove. Every caller is a label or a body
// whose whitespace is the provider's: the background-task row fields and the
// OSC notification body.
//
// It still removes every NON-whitespace control character, so an ESC, a bell
// and a bidirectional override cannot reach a tab strip or a notification.
//
// It never grows s, for the reason CleanNameChars does not: the loop decodes
// by hand, so it strips an invalid byte instead of writing a 3-byte U+FFFD in
// its place.
func StripUnreadable(s string, byteLimit int) string {
	var b strings.Builder
	if byteLimit > 0 {
		b.Grow(min(len(s), byteLimit))
	} else {
		b.Grow(len(s))
	}

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size

		if r == utf8.RuneError && size == 1 {
			continue
		}
		// Whitespace first. See the doc comment: six of these characters are
		// Cc as well, and a control test that claimed them first joined two
		// words into one.
		if !IsNameWhitespace(r) && IsUnreadable(r) {
			continue
		}
		if byteLimit > 0 && b.Len()+utf8.RuneLen(r) > byteLimit {
			break
		}
		b.WriteRune(r)
	}

	return b.String()
}

// SanitizeName sanitizes and validates a name/title string.
//
// It applies the CleanNameChars rule. That rule strips the control
// characters, the invisible format characters, and the invalid bytes. It
// folds each run of whitespace to one space, and it trims both ends.
// SanitizeName then returns an error when nothing survived, or when the
// result exceeds NameByteLimit bytes.
//
// The rule REWRITES as well as strips, so a caller that compares the result
// against its input refuses more than a character ban would: `Fira Code`
// holds a no-break space, which folds to a plain space and makes the two
// differ. Report the sanitized form in that error, so the user can see what
// the rule wants.
//
// Use CleanName instead at a write point that must accept whatever the caller
// sends, such as a tab title. This function is for a field whose error a user
// reads and corrects.
func SanitizeName(name string) (string, error) {
	sanitized := CleanNameChars(name, cleanScanLimit)
	if sanitized == "" {
		return "", fmt.Errorf("name must not be empty")
	}
	if len(sanitized) > NameByteLimit {
		return "", fmt.Errorf("name must be at most %d bytes", NameByteLimit)
	}
	return sanitized, nil
}

// CleanName returns name in the form that SanitizeName accepts. It never
// fails: it applies the SanitizeName character rule, then cuts what remains to
// NameByteLimit bytes. An empty return value means that no character survived
// -- the name was empty, or it held only whitespace and stripped characters --
// and each caller decides its own fallback for that case.
//
// The order is CLEAN FIRST, CUT SECOND. Do not reverse it. Cutting last makes
// the result fit the limit by construction, whatever the clean did, so
// SanitizeName's "too long" error is unreachable from here. It also keeps the
// text a user typed: a title of 200 invisible characters followed by "Plan"
// returns "Plan", rather than the empty string that a cut-first rule returned
// because the cut already took "Plan" with it.
//
// The result is idempotent -- CleanName(CleanName(s)) == CleanName(s) --
// because it holds no stripped character, no whitespace run longer than one
// space, no whitespace at either end, and at most NameByteLimit bytes. A
// second pass has nothing left to change.
//
// The trim runs again after the cut, because the cut can expose the one space
// that separated two words.
//
// Every writer of a tab title calls this, in the worker and in the browser
// alike, so the three title-writing RPCs and the derived plan title enforce
// one rule and none of them refuses a title. The browser copy is `cleanName`
// in `frontend/src/lib/validate.ts`; testdata/title_cleaning_conformance.json
// pins the two against each other.
func CleanName(name string) string {
	return CleanNameTo(name, NameByteLimit)
}

// CleanNameTo is CleanName with a caller-supplied byte limit. Every word of
// CleanName's contract holds, with byteLimit in place of NameByteLimit.
//
// It exists for a one-line label that is NOT a tab title and so does not want
// that limit: a background task's group heading is model-written prose of the
// same KIND as a title -- one line, read as a heading, with no structure of its
// own to keep -- but it is capped at LabelByteLimit beside its sibling label
// fields. Deriving both from one function keeps the rule in one place; passing
// the limit is what lets the two callers differ where they must.
//
// THE SCAN LIMIT TRAVELS WITH THE BYTE LIMIT, for the reason cleanScanLimit
// states: one byte past the limit is all the cut needs to see. A fixed scan
// limit would silently cap every result at NameByteLimit whatever byteLimit
// said, so a 512-byte label came back 129 bytes long.
func CleanNameTo(name string, byteLimit int) string {
	return strings.TrimSpace(TruncateToBytes(CleanNameChars(name, byteLimit+1), byteLimit))
}

// TruncateToBytes cuts s to at most limit UTF-8 bytes. It moves the cut back
// to the start of a rune, so the result never holds a partial rune. A limit of
// zero or less keeps nothing.
func TruncateToBytes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// SanitizeDisplayName sanitizes a display name, falling back to the given
// fallback value when the name is empty.
//
// The fallback test reads the RAW value, so a display name of one space, or
// of one zero width space, is not "empty" here: it reaches SanitizeName and
// becomes an error. That is what a FIELD wants, because the user reads the
// error and corrects it. A caller whose display name comes from a MACHINE --
// an identity provider, a config file -- has no such user, so it must clean
// the value at its own boundary with CleanName and pass "" on, which is what
// storePendingSignup does for an OAuth claim.
func SanitizeDisplayName(displayName, fallback string) (string, error) {
	if displayName == "" {
		displayName = fallback
	}
	return SanitizeName(displayName)
}
