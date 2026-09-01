/**
 * Every character that a name or a title loses to the strip.
 *
 * Two groups, and a reader sees neither of them:
 *
 * - The control blocks. Go's `unicode.IsControl` covers the Unicode Cc
 *   category, which is BOTH U+0000-U+001F and U+007F-U+009F. Leaving the C1
 *   half out let a name through that the hub then refused, with no explanation
 *   at the field.
 * - The format characters that occupy no width: the soft hyphen, the zero
 *   width space, the word joiner, the bidirectional marks, overrides and
 *   isolates, and U+FEFF. A reader cannot see one, so it can only hide text or
 *   pad a name past a limit that the visible characters fit. U+FEFF also
 *   settles the one input on which this rule and its Go copy disagreed:
 *   `String.prototype.trim` removes it and Go's `strings.TrimSpace` keeps it.
 *
 * The set is written out by CODE POINT rather than as `\p{Cf}`, because
 * `backend/util/validate/name.go` must strip the SAME characters, and Go and
 * each JavaScript engine update their Unicode tables on their own release
 * schedules. A category test would let the two sides disagree on a character
 * that one of them classifies first.
 * `testdata/title_cleaning_conformance.json` pins the two algorithms together.
 *
 * Three groups stay deliberately, although they are also invisible. U+200C
 * and U+200D stay, because the joiner builds an emoji family or a profession,
 * and both shape a word in Indic, Persian and Arabic orthography. The
 * variation selectors U+FE00-U+FE0F stay, because U+FE0F is what makes a
 * character render as an emoji. The tag characters U+E0020-U+E007F stay,
 * because they spell out a subdivision flag.
 *
 * Other invisible characters stay because nobody added them, and not because
 * a name may hold them: the invisible math operators U+2061-U+2064, the
 * interlinear annotation controls U+FFF9-U+FFFB, and the blank-glyph
 * characters U+115F, U+1160, U+2800 and U+3164 all survive today. Add a code
 * point to contracts/validate.json and a case to the shared fixture; both
 * sides' tables regenerate from the contract, and the fixture proves the
 * algorithms still agree.
 *
 * The control blocks have TWO gaps -- U+0009-U+000D and U+0085 -- and both
 * gaps are deliberate. Those characters are control characters AND whitespace
 * at the same time, so {@link NAME_WHITESPACE_G} below folds them to a space
 * instead. A strip here made `Fix parser\nAdd tests` read as
 * `Fix parserAdd tests`.
 *
 * The generator computes the gaps as the complement of {@link NAME_WHITESPACE_G}
 * inside the Cc category (NAME_INVISIBLE_CLASS is the derived `Cc minus the
 * whitespace folds` class), and the Go copy computes the same overlap by
 * testing whitespace before control. Narrow the fold class and a gap becomes
 * a hole: a control character then reaches the stored name, and the hub
 * refuses it with no explanation at the field. `cleanName leaves no C0 or C1
 * control in the result` in `./validate.test.ts` walks every code point from
 * U+0000 to U+009F and is what turns red for it.
 *
 * `"`, `\`, `$` and `%` are NOT here. No sink reads a stored name as syntax:
 * the shell path quotes each argument, the stylesheet path escapes at the
 * emitter (`buildFontFamily` in `~/lib/fontStack`), the plan file name keeps
 * letters and digits only, and the SQL is parameterized. A guard at the
 * emitter holds for whatever the store holds; a character ban here only
 * removed the user's text.
 */

import { BRANCH_FORBIDDEN_CLASS, BRANCH_NAME_BYTE_LIMIT, MAX_PASSWORD_LENGTH, MIN_PASSWORD_LENGTH, NAME_BYTE_LIMIT, NAME_INVISIBLE_CLASS, NAME_WHITESPACE_CLASS, NAME_WHITESPACE_MINUS_SPACE_CLASS, PRINTABLE_ASCII_CLASS, PUBLIC_RESERVED_USERNAMES, SESSION_FILE_PATH_BYTE_LIMIT, SESSION_FORBIDDEN_CLASS, SESSION_ID_BYTE_LIMIT, SESSION_INVISIBLE_CLASS, SYSTEM_RESERVED_USERNAMES } from '~/generated/contracts/validate'

const NAME_INVISIBLE_G = new RegExp(`[${NAME_INVISIBLE_CLASS}]`, 'g')

/**
 * A surrogate code unit with no partner.
 *
 * A JavaScript string can hold one; a UTF-8 string cannot represent one, so
 * `TextEncoder` turns it into U+FFFD and the byte count below would measure a
 * character the hub never receives. The Go copy drops an invalid byte for the
 * same reason. Dropping it also keeps the rule from ever GROWING a string,
 * which is what lets the cut run last.
 */
const LONE_SURROGATE_G = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g

/**
 * Every character a name rule folds to one space, as a regex character-class
 * body. {@link NAME_WHITESPACE_G} and {@link EDGE_WHITESPACE} are both built
 * from it, so the set has one spelling on this side.
 *
 * It is written out by CODE POINT rather than as `\s`, for the reason
 * {@link NAME_INVISIBLE_G} is, and the reason applies with MORE force here.
 * `\s` moves with the JavaScript engine's Unicode version and Go's
 * `unicode.IsSpace` moves with the Go release, so a Space_Separator added to
 * Unicode later would reach the browser and the worker in different weeks:
 * one side folds a character the other keeps, and the tab strip shows one
 * title while the worker stores another with no error anywhere. The Cc
 * category is frozen, so the STRIP was already deterministic; the fold was
 * the last half of this rule that two runtimes could answer differently.
 *
 * The set is Go's `unicode.IsSpace`, which is `\s` with two edits. U+0085 is
 * IN, because Go claims it and `\s` does not. U+FEFF is OUT, because `\s`
 * claims it and Go does not -- and {@link NAME_INVISIBLE_G} strips it before
 * this pass reads the string anyway.
 *
 * Pinning makes the set stale on purpose: a Space_Separator added later
 * renders as a visible character inside a title on BOTH sides until somebody
 * adds it here. That failure is visible, and the drift it replaces was
 * silent. The `matches Go's whitespace set` test reports the day the pinned
 * set and `\s` stop differing by exactly those two code points.
 */
/** A run of whitespace, which becomes one space. */
const NAME_WHITESPACE_G = new RegExp(`[${NAME_WHITESPACE_CLASS}]+`, 'g')

/**
 * Whitespace at either end of a value, for a rule that REFUSES rather than
 * folds.
 *
 * `validateSessionId` uses this and not `String.prototype.trim`, because
 * `trim` reads the engine's own whitespace set. Go's `strings.TrimSpace`
 * reads a different one, so the two would ACCEPT and REFUSE the same token
 * once either runtime claims a new Space_Separator first \u2014 a browser that
 * offers a resume the worker then rejects.
 */
const EDGE_WHITESPACE = new RegExp(`^[${NAME_WHITESPACE_CLASS}]|[${NAME_WHITESPACE_CLASS}]$`)

// Every whitespace character the pinned class holds EXCEPT the plain U+0020.
// The generator computes the subtraction (NAME_WHITESPACE_MINUS_SPACE_CLASS),
// so a character added to `NAME_WHITESPACE_CLASS` is covered here the same
// day, and the Go copy reads its own pinned table the same way
// (`r != ' ' && IsNameWhitespace(r)`).
const INTERIOR_NON_PLAIN_WHITESPACE = new RegExp(
  `[${NAME_WHITESPACE_MINUS_SPACE_CLASS}]`,
)

// Length is measured in UTF-8 bytes, so hoist one encoder rather than
// constructing one for each call.

const UTF8 = new TextEncoder()

/**
 * The character rule that every name and title shares: strip what a reader
 * cannot see, fold each run of whitespace to one space, and trim both ends.
 *
 * It never GROWS the string, and {@link cleanName} depends on that. Each pass
 * either removes code units or replaces a run of them with a single space.
 *
 * The passes run in THIS order. A fold that ran first would leave a U+200B
 * between two spaces untouched, because U+200B is not `\s`, and the strip
 * would then produce the double space that the fold was supposed to collapse.
 * `trim` runs last, because the fold leaves at most one space at each end.
 *
 * The strip and the fold read DISJOINT sets, which is what lets two passes do
 * the work of the Go copy's one. {@link NAME_INVISIBLE_G} leaves every
 * whitespace character alone, and {@link NAME_WHITESPACE_G} claims all of them.
 */
function cleanNameChars(name: string): string {
  return name
    .replace(LONE_SURROGATE_G, '')
    .replace(NAME_INVISIBLE_G, '')
    .replace(NAME_WHITESPACE_G, ' ')
    .trim()
}

/**
 * Sanitizes and validates a name/title string.
 *
 * Applies the {@link cleanNameChars} rule. That rule strips the control
 * characters, the invisible format characters, and the lone surrogates. It
 * folds each run of whitespace to one space, and it trims both ends.
 * `sanitizeName` then reports an error when nothing survived, or when the
 * result exceeds 128 bytes.
 *
 * The rule REWRITES as well as strips, so a caller that compares the result
 * against its input refuses more than a character ban would: `Fira Code`
 * holds a no-break space, which folds to a plain space and makes the two
 * differ.
 *
 * This is the browser copy of `backend/util/validate/name.go`, and the hub
 * REFUSES a name that differs from its own sanitized form rather than
 * storing the stripped one — so the two rules must agree exactly. The limit
 * counts UTF-8 BYTES because Go's `len` does: 128 characters of CJK is 384
 * bytes, which a character count accepted here and the hub then refused.
 *
 * Use {@link cleanName} for a tab title. That path never refuses.
 */
export function sanitizeName(name: string): { value: string, error: string | null } {
  const value = cleanNameChars(name)
  let error: string | null = null
  if (value === '') {
    error = 'Name must not be empty'
  }
  else if (UTF8.encode(value).length > NAME_BYTE_LIMIT) {
    error = `Name must be at most ${NAME_BYTE_LIMIT} bytes`
  }
  return { value, error }
}

/** The UTF-8 size of one code point, in bytes. */
function utf8Size(codePoint: number): number {
  if (codePoint < 0x80)
    return 1
  if (codePoint < 0x800)
    return 2
  if (codePoint < 0x10000)
    return 3
  return 4
}

/**
 * Cuts `value` to at most `limit` UTF-8 bytes, at a character boundary.
 *
 * `for...of` walks a string by CODE POINT, so an astral character is one step
 * and the cut never lands between the two halves of a surrogate pair.
 * `String.prototype.slice` counts UTF-16 code units instead, and slicing at
 * byte 128 both measures the wrong unit and splits a pair — the fixture case
 * `the cut lands inside a surrogate pair` is what refuses that shortcut.
 *
 * The loop is the ONLY measurement. A `UTF8.encode(value).length <= limit`
 * guard in front of it read the same length a second way, and it allocated a
 * byte array for the whole string to do so — on a pasted title of a megabyte,
 * for a question the loop answers after `limit` steps.
 */
function truncateToBytes(value: string, limit: number): string {
  if (limit <= 0)
    return ''

  let bytes = 0
  let units = 0
  for (const char of value) {
    const size = utf8Size(char.codePointAt(0)!)
    if (bytes + size > limit)
      return value.slice(0, units)
    bytes += size
    // `char.length` is 1 for a BMP character and 2 for an astral one, so this
    // counts the UTF-16 units that `slice` above wants.
    units += char.length
  }
  return value
}

/**
 * Cleans a name/title, and never refuses one. Applies the
 * {@link sanitizeName} character rule, then cuts what remains to
 * {@link NAME_BYTE_LIMIT} bytes. An empty return value means that no character
 * survived, and each caller decides its own fallback for that case.
 *
 * This is the browser copy of `validate.CleanName`, and
 * `testdata/title_cleaning_conformance.json` pins the two against each other.
 * Every tab title goes through it, in the browser and in the worker alike, so
 * the title the user sees after a rename is the title the worker stores.
 *
 * The order is CLEAN FIRST, CUT SECOND. Do not reverse it. Cutting last makes
 * the result fit the limit by construction, which is what makes
 * `sanitizeName`'s "too long" error unreachable here. It also keeps the text
 * the user typed: a title of 200 invisible characters followed by `Plan`
 * returns `Plan`, where a cut-first rule already took `Plan` away.
 *
 * The result is idempotent, because it holds no stripped character, no
 * whitespace run longer than one space, no whitespace at either end, and at
 * most {@link NAME_BYTE_LIMIT} bytes.
 *
 * The trim runs again after the cut, because the cut can expose the one space
 * that separated two words.
 */
export function cleanName(name: string): string {
  return truncateToBytes(cleanNameChars(name), NAME_BYTE_LIMIT).trim()
}

/**
 * Sanitizes a display name, falling back to the given fallback when empty.
 */
export function sanitizeDisplayName(displayName: string, fallback: string): { value: string, error: string | null } {
  return sanitizeName(displayName || fallback)
}

/**
 * A character forbidden in a git branch name: the space, `~ ^ : ? * [ ] \`, and
 * the control blocks.
 *
 * This is what GIT refuses, and nothing more. `$`, `%` and `]` are absent
 * because git accepts them: refusing them made the panel reject an existing
 * branch that the repository already holds and that `for-each-ref` lists, so
 * the user saw a name they could not act on. git forbids `[` and not `]`.
 *
 * The class stops at U+007F. git refuses the ASCII controls and DEL; the C1
 * block U+0080-U+009F is not a git rule. The worker copy asked
 * `unicode.IsControl`, which reports the whole Unicode Cc category, and this
 * class was widened to match that over-strictness rather than to match git --
 * both are now narrowed to git's own rule.
 */

const BRANCH_FORBIDDEN_CHARS = new RegExp(`[${BRANCH_FORBIDDEN_CLASS}]`)

/**
 * Validates a git branch name according to git-check-ref-format rules.
 * Returns an error message string, or null if valid.
 *
 * This is the browser copy of `gitutil.ValidateBranchName`, and the two must
 * refuse the same names: the panel offers a branch that the worker then
 * refuses, or the reverse, and the user reads two answers for one name. The
 * limit counts UTF-8 BYTES because Go's `len` does — an 86-character CJK name
 * is 258 bytes, which a `String.length` count accepted here and the worker
 * then refused.
 */
export function validateBranchName(name: string): string | null {
  if (name === '') {
    return 'Branch name must not be empty'
  }
  if (UTF8.encode(name).length > BRANCH_NAME_BYTE_LIMIT) {
    return `Branch name must be at most ${BRANCH_NAME_BYTE_LIMIT} bytes`
  }
  if (BRANCH_FORBIDDEN_CHARS.test(name)) {
    return 'Branch name contains invalid characters'
  }
  // `@` alone is the one refname git refuses; a LEADING `@` is legal.
  if (name === '@') {
    return 'Branch name must not be the single character @'
  }
  // The leading `-` is the one refusal that goes BEYOND git, and it is
  // deliberate: the worker hands the name to git as a positional argument, and
  // git's option parser would read a leading `-` as a flag.
  if (name.startsWith('/') || name.startsWith('.') || name.startsWith('-')) {
    return 'Branch name must not start with /, ., or -'
  }
  if (name.endsWith('/') || name.endsWith('.')) {
    return 'Branch name must not end with / or .'
  }
  // git refuses `.lock` on EVERY slash-separated component, not on the last one
  // alone: `a.lock/b` is not a valid ref.
  if (name.split('/').some(comp => comp.endsWith('.lock'))) {
    return 'Branch name must not have a path component that ends with .lock'
  }
  // `@{` is git's reflog syntax and is refused anywhere in a ref name.
  if (name.includes('@{')) {
    return 'Branch name must not contain @{'
  }
  if (name.includes('..')) {
    return 'Branch name must not contain ..'
  }
  if (name.includes('//')) {
    return 'Branch name must not contain //'
  }
  if (name.includes('/.')) {
    return 'Branch name must not contain /.'
  }
  return null
}

/**
 * Returns true if the branch name is valid.
 */
export function isValidBranchName(name: string): boolean {
  return validateBranchName(name) === null
}

/**
 * Strips the remote prefix from a branch ref so the local-branch name
 * remains: `"origin/foo"` → `"foo"`. A bare local name is returned
 * unchanged. Matches the worker's `checkoutBranchInDir`, which checks
 * out remote-tracking refs as the suffix after the first `/`.
 */
export function stripRemotePrefix(ref: string): string {
  const slash = ref.indexOf('/')
  return slash === -1 ? ref : ref.slice(slash + 1)
}

/**
 * A character an agent session ID must not hold: the control blocks, the
 * invisible format characters, and the four characters the name rule used to
 * strip.
 *
 * Written out here rather than borrowed from {@link NAME_INVISIBLE_G}, so
 * that a later change to what a NAME may hold cannot widen or narrow what a
 * TOKEN may hold. The repetition is the point.
 *
 * The class carries the invisible format characters, and not U+FEFF alone,
 * because every one of them travels through a copy and a paste unseen. A
 * session ID that carries one names no session that the agent knows, and the
 * resume then starts a new conversation with no report of why.
 */

const SESSION_ID_FORBIDDEN = new RegExp(`[${SESSION_FORBIDDEN_CLASS}]`)

/**
 * Validates a session ID for resuming an agent session.
 * Returns an error message string, or null if valid.
 *
 * This rule REFUSES rather than strips, and it owns its own character class.
 * A session ID is an opaque token that the worker hands to the agent: it
 * becomes an argv element of `claude --resume <id>` and the `sessionId` member
 * of an ACP request. A rewritten token resumes a different session, or no
 * session, so the only correct answer for a character this rule does not want
 * is to report it. `validate.ValidateSessionID` refuses the same way, for the
 * same reason.
 *
 * The tests run in THIS order, and the Go copy runs them in the same order,
 * because the two languages disagree about which characters a trim removes:
 * `String.prototype.trim` claims U+FEFF and Go's `strings.TrimSpace` does
 * not, and Go's claims U+0085 where `trim` does not. A trim-first rule
 * therefore reported "must not start or end with whitespace" on one side and
 * "contains invalid characters" on the other, for one input.
 * `testdata/session_id_conformance.json` pins the order together with the
 * class.
 *
 * The edge test reads {@link EDGE_WHITESPACE} and not `trim`, which is the
 * second half of that problem: a trim reads the runtime's OWN whitespace set,
 * so a Space_Separator that one runtime claims first would make the browser
 * accept a token the worker refuses — an accept/refuse split rather than the
 * message split the ordering fixed.
 *
 * The lone-surrogate test is here because `TextEncoder` rewrites an unpaired
 * surrogate to U+FFFD: the browser would otherwise measure and then send a
 * token the user never typed, and the hub reads U+FFFD as ordinary text. The
 * Go copy refuses an invalid byte for the same reason.
 *
 * The leading-hyphen test is last, and it is here because argv cannot tell a
 * hyphen-prefixed token from a flag: the worker passes the token to
 * `claude --resume <id>` as its own argv element, `--resume` takes an optional
 * value, and a parser of that shape reads the token as a flag of its own
 * instead. No provider issues an identifier that starts with a hyphen, so the
 * field refuses one here rather than after a round trip.
 *
 * The empty value is accepted here and means "no resume". The caller that
 * requires one checks for it separately.
 */
export function validateSessionId(value: string): string | null {
  if (value === '')
    return null
  if (UTF8.encode(value).length > SESSION_ID_BYTE_LIMIT)
    return `Session ID must be at most ${SESSION_ID_BYTE_LIMIT} bytes`
  // `LONE_SURROGATE_G` carries the `g` flag for its use in `replace`, and
  // `RegExp.prototype.test` on a global regex advances `lastIndex` and answers
  // from there on the NEXT call. Reset it, so this test reads the whole value
  // every time.
  LONE_SURROGATE_G.lastIndex = 0
  if (LONE_SURROGATE_G.test(value))
    return 'Session ID contains invalid characters'
  if (SESSION_ID_FORBIDDEN.test(value))
    return 'Session ID contains invalid characters'
  if (EDGE_WHITESPACE.test(value))
    return 'Session ID must not start or end with whitespace'
  // Whitespace that is NOT the plain space, anywhere inside the token. AFTER
  // the edge test, so a leading or trailing one reports the edge rule that
  // tells the user what to fix. The Go copy runs the two in the same order.
  //
  // `\n` and `\t` are already refused as control characters, but U+2028 means
  // the same thing and is not one, and U+00A0 and U+3000 render as a space
  // while carrying different bytes -- two tokens a reader cannot tell apart,
  // naming different sessions. The plain U+0020 stays legal inside a token.
  if (INTERIOR_NON_PLAIN_WHITESPACE.test(value))
    return 'Session ID contains invalid characters'
  if (value.startsWith('-'))
    return 'Session ID must not start with a hyphen'
  return null
}

/**
 * The start of an absolute path, in EVERY spelling any worker OS accepts: a
 * POSIX root, a UNC or backslash root, a tilde with a separator, and a Windows
 * drive letter.
 *
 * It accepts the UNION and lets the worker's own rule
 * (`validate.SanitizePath`, which asks `filepath.IsAbs` for THAT host) settle
 * it. The union can only accept more than the worker does, never less, which
 * is the direction this field must fail in: a value the browser refuses never
 * reaches the worker to be judged.
 *
 * The union is a deliberate choice and not a missing fact. The browser CAN
 * learn the worker's OS — `GetWorkerSystemInfoResponse` carries it, and
 * `workerInfoStore.getOs` already feeds `flavorFromOs` for the directory field
 * in this same dialog. Narrowing per host would refuse a wrong-host spelling
 * one round trip sooner, at the cost of a rule that answers differently before
 * and after the worker-info fetch resolves, and of a second `browser` column
 * in `testdata/pi_resume_handle_conformance.json`. The worker stays
 * authoritative either way, so the union costs a round trip and nothing else.
 *
 * A tilde ALONE is not here, because it never reaches this test: it holds no
 * separator, so the shape test above sends it to the token rule. The worker
 * splits the same value the same way.
 */
const ABSOLUTE_PATH_START = /^(?:[/\\]|~[/\\]|[a-z]:[/\\])/i

/** A `..` component, in a path written with either separator. */
const PATH_TRAVERSAL = /(?:^|[/\\])\.\.(?:[/\\]|$)/

/**
 * The invisible-format characters a session file path may not hold.
 *
 * The path rule cannot reuse the whole token class, which bans the backslash a
 * Windows path needs. It refuses THIS half because the worker's `SanitizePath`
 * does not: that rule drops control characters and trims edge whitespace, but
 * U+200B is Cf rather than Cc, so it travels through untouched and reaches the
 * agent inside a filename.
 */
const SESSION_INVISIBLE = new RegExp(`[${SESSION_INVISIBLE_CLASS}]`)

/**
 * Validates a value already known to be a session FILE PATH. The empty value
 * is accepted and means "no resume".
 *
 * This is the generic half of a two-shape resume handle, and it is deliberately
 * provider-neutral: WHICH values are paths is a provider's own resolver rule
 * and lives in that provider's plugin (`validateResumeHandle` — Pi's is the
 * only one today). What a session file path may look like is the same question
 * for any provider that has one.
 *
 * It is DELIBERATELY narrower than the worker's rule. It refuses only what is
 * wrong on every host -- a relative path, a `..` escape, an invisible-format
 * character, a value past the byte cap -- and leaves the Windows device names
 * and the OS-specific spelling of "absolute" to the worker, which knows its own
 * host. A browser copy of those would refuse paths a POSIX worker accepts,
 * which is the failure this rule exists to remove. So a value this accepts may
 * still be refused by the worker; a value this REFUSES must be refused by every
 * worker, because it never reaches one to be judged.
 */
export function validateSessionFilePath(value: string): string | null {
  if (value === '')
    return null
  if (UTF8.encode(value).length > SESSION_FILE_PATH_BYTE_LIMIT)
    return `Session file path must be at most ${SESSION_FILE_PATH_BYTE_LIMIT} bytes`
  // The one refusal the path rule keeps from the token rule. The worker's
  // `SanitizePath` DROPS a control character and TRIMS edge whitespace before
  // it judges, so those need no test here — but an invisible-format character
  // is neither: U+200B is Cf, so it travels through untouched and reaches the
  // agent inside a filename, naming a session that does not exist. The worker
  // refuses the same class (`validate.RefuseInvisibleSessionChars`).
  if (SESSION_INVISIBLE.test(value))
    return 'Session file path contains invisible characters'
  if (!ABSOLUTE_PATH_START.test(value))
    return 'Session file path must be absolute'
  if (PATH_TRAVERSAL.test(value))
    return 'Session file path must not contain ".."'
  return null
}

/**
 * Validates an email address format.
 * Returns an error message string, or null if valid.
 * Empty strings are accepted (use a separate required check if needed).
 */
const EMAIL_INVALID_CHARS = /[\s<>,]/

export function validateEmail(email: string): string | null {
  if (email === '')
    return null
  if (email.length > 254)
    return 'Email must be at most 254 characters'
  // Basic structural check: local@domain.tld
  const at = email.indexOf('@')
  if (at < 1)
    return 'Invalid email address'
  const domain = email.slice(at + 1)
  if (!domain.includes('.'))
    return 'Invalid email address'
  // Reject whitespace, angle brackets, commas (display-name style).
  if (EMAIL_INVALID_CHARS.test(email))
    return 'Invalid email address'
  return null
}

// Password length limits, counted in characters. A password holds printable
// ASCII characters only (see validatePassword), so one character is one
// UTF-16 code unit and `String.length` counts characters here. The limits
// themselves are the generated MIN_PASSWORD_LENGTH / MAX_PASSWORD_LENGTH.

/**
 * Matches a string in which EVERY UTF-16 code unit is printable ASCII: 0x20
 * (the space) through 0x7E (the tilde). The test is positive — it states what
 * a password may hold rather than enumerating what it may not — so no control
 * character appears in the pattern and the `no-control-regex` lint rule has
 * nothing to suppress. An astral character (an emoji) is caught because each
 * of its surrogates is a code unit above 0x7E. The empty string matches, and
 * the minimum-length rule below is the one that reports it.
 */
const PRINTABLE_ASCII_ONLY = new RegExp(`^[${PRINTABLE_ASCII_CLASS}]*$`)

/**
 * Validates a password against the character-set policy and the length
 * policy. Returns an error message string, or null if valid.
 *
 * This is the browser copy of `backend/util/validate/password.go`, and the
 * hub refuses whatever this function refuses, so the two rules must agree
 * exactly. A password holds printable ASCII characters only: 0x20 (the
 * space) through 0x7E (the tilde).
 *
 * The upper half of the range refuses every character above 0x7E, because
 * the two sides cannot otherwise agree on what a length limit counts: Go's
 * `len` counts UTF-8 BYTES and `String.length` counts UTF-16 CODE UNITS. A
 * 43 character CJK password is 43 code units and 129 bytes, so this function
 * accepted it and the hub then refused it as too long. ASCII makes one
 * character one code unit and one byte at the same time, so the two limits
 * become one rule.
 *
 * The lower half refuses the control block 0x00-0x1F and DEL (0x7F). A
 * control character reaches a password field through a paste accident or a
 * terminal control sequence, never through deliberate typing, and a password
 * the user cannot type again is a lockout. The space (0x20) stays ALLOWED: a
 * passphrase with spaces is a good password, and neither side trims a
 * password.
 *
 * `testdata/password_policy_conformance.json` is the fixture both sides run,
 * and it pins each boundary of this range.
 */
export function validatePassword(password: string): string | null {
  // The character-set rule runs FIRST, because its refusal is the actionable
  // one. A user who counted 3 CJK characters cannot act on a minimum of 8
  // that the hub measured in bytes.
  if (!PRINTABLE_ASCII_ONLY.test(password))
    return 'Password must contain only printable ASCII characters (the space is allowed)'
  if (password.length < MIN_PASSWORD_LENGTH)
    return `Password must be at least ${MIN_PASSWORD_LENGTH} characters`
  if (password.length > MAX_PASSWORD_LENGTH)
    return `Password must be at most ${MAX_PASSWORD_LENGTH} characters`
  return null
}

const SLUG_PATTERN = /^[a-z0-9-]+$/

/**
 * Sanitizes and validates a GitHub-style slug (username).
 * Trims whitespace and lowercases, then validates.
 * Rules: 1-32 chars, lowercase alphanumeric and hyphens only,
 * no leading/trailing hyphens, no consecutive hyphens.
 * Returns [cleanedSlug, null] on success, or ['', errorMessage] on failure.
 */
export function sanitizeSlug(fieldName: string, value: string): [string, string | null] {
  const slug = value.trim().toLowerCase()
  if (slug === '') {
    return ['', `${fieldName} must not be empty`]
  }
  if (slug.length > 32) {
    return ['', `${fieldName} must be at most 32 characters`]
  }
  if (!SLUG_PATTERN.test(slug)) {
    return ['', `${fieldName} must contain only letters, numbers, and hyphens`]
  }
  if (slug.startsWith('-')) {
    return ['', `${fieldName} must not start with a hyphen`]
  }
  if (slug.endsWith('-')) {
    return ['', `${fieldName} must not end with a hyphen`]
  }
  if (slug.includes('--')) {
    return ['', `${fieldName} must not contain consecutive hyphens`]
  }
  return [slug, null]
}

// Reserved usernames are generated from contracts/validate.json, the same
// file the hub's usernames package reads: systemReserved covers every
// creation path, publicReserved only anonymous public signup. The generated
// arrays are one element long each, so membership reads `.includes` directly.

/**
 * Returns an error message if the username is reserved for the given context,
 * or null otherwise. Pass `allowAdmin=true` for first-admin setup forms; use
 * the default (`false`) for public signup and OAuth-completion paths.
 */
export function validateReservedUsername(slug: string, allowAdmin: boolean): string | null {
  const normalized = slug.trim().toLowerCase()
  if (SYSTEM_RESERVED_USERNAMES.includes(normalized)) {
    return `"${normalized}" is a reserved username`
  }
  if (!allowAdmin && PUBLIC_RESERVED_USERNAMES.includes(normalized)) {
    return `"${normalized}" is a reserved username`
  }
  return null
}
