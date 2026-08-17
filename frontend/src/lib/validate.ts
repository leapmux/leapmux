// The C1 block (U+0080-U+009F) is here because the hub strips every rune
// `unicode.IsControl` reports, and the Unicode Cc category is BOTH
// U+0000-U+001F and U+007F-U+009F. Leaving the C1 half out let a name
// through that the hub then refused, with no explanation at the field.
//
// U+FEFF (the byte order mark) is here for the opposite reason: Go's
// `unicode.IsControl` reports FALSE for it, because its category is Cf. It has
// to go anyway, since `String.prototype.trim` removes it and Go's
// `strings.TrimSpace` keeps it — a pasted byte order mark was the one input on
// which this rule and its Go copy disagreed.
// eslint-disable-next-line no-control-regex
const NAME_FORBIDDEN_G = /[\x00-\x1F\x7F-\x9F"\\$%\uFEFF]/g

/**
 * The maximum size of a name or a title, in UTF-8 BYTES. Mirrors
 * `validate.NameByteLimit`.
 */
export const NAME_BYTE_LIMIT = 128

// Length is measured in UTF-8 bytes, so hoist one encoder rather than
// constructing one for each call.
const UTF8 = new TextEncoder()

/**
 * Sanitizes and validates a name/title string.
 * Forbidden characters (control characters, the byte order mark, ", \, $, %)
 * are silently stripped. Returns the sanitized string and an error if the
 * result is empty or exceeds 128 bytes.
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
  const value = name.replace(NAME_FORBIDDEN_G, '').trim()
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
 */
function truncateToBytes(value: string, limit: number): string {
  if (limit <= 0)
    return ''
  if (UTF8.encode(value).length <= limit)
    return value

  let bytes = 0
  let units = 0
  for (const char of value) {
    const size = utf8Size(char.codePointAt(0)!)
    if (bytes + size > limit)
      break
    bytes += size
    // `char.length` is 1 for a BMP character and 2 for an astral one, so this
    // counts the UTF-16 units that `slice` below wants.
    units += char.length
  }
  return value.slice(0, units)
}

/**
 * Cleans a name/title, and never refuses one. Cuts it to
 * {@link NAME_BYTE_LIMIT} bytes, then applies the {@link sanitizeName}
 * character rule to what remains. An empty return value means that no
 * character survived, and each caller decides its own fallback for that case.
 *
 * This is the browser copy of `validate.CleanName`, and
 * `testdata/title_cleaning_conformance.json` pins the two against each other.
 * Every tab title goes through it, in the browser and in the worker alike, so
 * the title the user sees after a rename is the title the worker stores. The
 * order is load-bearing: the strip only REMOVES bytes, so a cut string still
 * fits the limit, which is what makes `sanitizeName`'s "too long" error
 * unreachable here and makes the result idempotent.
 *
 * The discarded `error` is the empty-result one, which the empty return value
 * already reports.
 */
export function cleanName(name: string): string {
  return sanitizeName(truncateToBytes(name, NAME_BYTE_LIMIT)).value
}

/**
 * Sanitizes a display name, falling back to the given fallback when empty.
 */
export function sanitizeDisplayName(displayName: string, fallback: string): { value: string, error: string | null } {
  return sanitizeName(displayName || fallback)
}

// Characters forbidden in git branch names: space ~ ^ : ? * [ ] \ $ %
// Also control characters (0x00-0x1F, 0x7F).
// eslint-disable-next-line no-control-regex
const BRANCH_FORBIDDEN_CHARS = /[\x00-\x1F\x7F ~^:?*[\]\\$%]/

/**
 * Validates a git branch name according to git-check-ref-format rules.
 * Returns an error message string, or null if valid.
 */
export function validateBranchName(name: string): string | null {
  if (name === '') {
    return 'Branch name must not be empty'
  }
  if (name.length > 256) {
    return 'Branch name must be at most 256 characters'
  }
  if (BRANCH_FORBIDDEN_CHARS.test(name)) {
    return 'Branch name contains invalid characters'
  }
  if (name.startsWith('/') || name.startsWith('.') || name.startsWith('-') || name.startsWith('@')) {
    return 'Branch name must not start with /, ., -, or @'
  }
  if (name.endsWith('/') || name.endsWith('.') || name.endsWith('.lock')) {
    return 'Branch name must not end with /, ., or .lock'
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
 * Validates a session ID for resuming an agent session.
 * Delegates to sanitizeName for control-character and length checks,
 * and additionally rejects input that contains forbidden characters.
 * Returns an error message string, or null if valid.
 */
export function validateSessionId(value: string): string | null {
  const { value: sanitized, error } = sanitizeName(value)
  if (error)
    return error
  if (sanitized !== value)
    return 'Session ID contains invalid characters.'
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
// UTF-16 code unit and `String.length` counts characters here.
const MIN_PASSWORD_LENGTH = 8
const MAX_PASSWORD_LENGTH = 128

/**
 * Matches a string in which EVERY UTF-16 code unit is printable ASCII: 0x20
 * (the space) through 0x7E (the tilde). The test is positive — it states what
 * a password may hold rather than enumerating what it may not — so no control
 * character appears in the pattern and the `no-control-regex` lint rule has
 * nothing to suppress. An astral character (an emoji) is caught because each
 * of its surrogates is a code unit above 0x7E. The empty string matches, and
 * the minimum-length rule below is the one that reports it.
 */
const PRINTABLE_ASCII_ONLY = /^[\u0020-\u007E]*$/

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

/**
 * Usernames reserved across every creation path (public signup, setup,
 * OAuth signup, admin CLI). Mirrors backend `usernames.IsReservedSystem`.
 */
const SYSTEM_RESERVED_USERNAMES: ReadonlySet<string> = new Set(['solo'])

/**
 * Usernames reserved only for anonymous post-setup signup. Mirrors backend
 * `usernames.IsReservedPublic`.
 */
const PUBLIC_RESERVED_USERNAMES: ReadonlySet<string> = new Set(['admin'])

/**
 * Returns an error message if the username is reserved for the given context,
 * or null otherwise. Pass `allowAdmin=true` for first-admin setup forms; use
 * the default (`false`) for public signup and OAuth-completion paths.
 */
export function validateReservedUsername(slug: string, allowAdmin: boolean): string | null {
  const normalized = slug.trim().toLowerCase()
  if (SYSTEM_RESERVED_USERNAMES.has(normalized)) {
    return `"${normalized}" is a reserved username`
  }
  if (!allowAdmin && PUBLIC_RESERVED_USERNAMES.has(normalized)) {
    return `"${normalized}" is a reserved username`
  }
  return null
}
