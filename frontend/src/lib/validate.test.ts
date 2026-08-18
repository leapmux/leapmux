import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import {
  cleanName,
  isValidBranchName,
  NAME_BYTE_LIMIT,
  NAME_WHITESPACE_CLASS,
  sanitizeDisplayName,
  sanitizeName,
  sanitizeSlug,
  SESSION_ID_BYTE_LIMIT,
  stripRemotePrefix,
  validateBranchName,
  validateEmail,
  validatePassword,
  validateReservedUsername,
  validateSessionId,
} from './validate'

describe('sanitizeName', () => {
  it('returns sanitized value for valid names', () => {
    expect(sanitizeName('hello')).toEqual({ value: 'hello', error: null })
    expect(sanitizeName('hello world')).toEqual({ value: 'hello world', error: null })
    expect(sanitizeName('my-name')).toEqual({ value: 'my-name', error: null })
    expect(sanitizeName('my_name')).toEqual({ value: 'my_name', error: null })
    expect(sanitizeName('my.name')).toEqual({ value: 'my.name', error: null })
    expect(sanitizeName('name123')).toEqual({ value: 'name123', error: null })
    expect(sanitizeName('My Name-1.0_beta')).toEqual({ value: 'My Name-1.0_beta', error: null })
  })

  it('accepts names with special characters', () => {
    expect(sanitizeName('name@here').error).toBeNull()
    expect(sanitizeName('hello!').error).toBeNull()
    expect(sanitizeName('path/name').error).toBeNull()
    expect(sanitizeName('it\'s fine').error).toBeNull()
    expect(sanitizeName('a + b = c').error).toBeNull()
    expect(sanitizeName('project (draft)').error).toBeNull()
  })

  it('accepts unicode characters', () => {
    expect(sanitizeName('café').error).toBeNull()
  })

  it('accepts emoji', () => {
    expect(sanitizeName('hello\u{1F600}').error).toBeNull()
  })

  it('accepts names at max length (128 bytes)', () => {
    expect(sanitizeName('a'.repeat(128)).error).toBeNull()
  })

  it('trims whitespace in returned value', () => {
    const result = sanitizeName('  hello  ')
    expect(result.value).toBe('hello')
    expect(result.error).toBeNull()
  })

  it('strips a control character and returns the sanitized value', () => {
    expect(sanitizeName('hello\x00world')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\x1Fworld')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\x7Fworld')).toEqual({ value: 'helloworld', error: null })
  })

  // The four characters this rule used to strip. Each one is ordinary text:
  // no sink reads a stored name as syntax, so the ban only removed what the
  // user typed.
  it('keeps a quote, a backslash, a dollar and a percent', () => {
    expect(sanitizeName('name"quoted')).toEqual({ value: 'name"quoted', error: null })
    expect(sanitizeName('back\\slash')).toEqual({ value: 'back\\slash', error: null })
    expect(sanitizeName('hello$world')).toEqual({ value: 'hello$world', error: null })
    expect(sanitizeName('100%done')).toEqual({ value: '100%done', error: null })
    expect(sanitizeName('npm test --grep "$FOO"')).toEqual({ value: 'npm test --grep "$FOO"', error: null })
    expect(sanitizeName('C:\\Users\\me')).toEqual({ value: 'C:\\Users\\me', error: null })
  })

  // Whitespace FOLDS rather than vanishing, so a pasted two-line title does
  // not run its two lines together.
  it('folds a run of whitespace to one space', () => {
    expect(sanitizeName('hello\tworld')).toEqual({ value: 'hello world', error: null })
    expect(sanitizeName('hello\nworld')).toEqual({ value: 'hello world', error: null })
    expect(sanitizeName('hello\r\nworld')).toEqual({ value: 'hello world', error: null })
    expect(sanitizeName('hello    world')).toEqual({ value: 'hello world', error: null })
    expect(sanitizeName('hello\u00A0world')).toEqual({ value: 'hello world', error: null })
  })

  // The invisible format characters. A reader cannot see one, so it can only
  // hide text or pad a name past a limit that the visible characters fit.
  it('strips an invisible format character', () => {
    expect(sanitizeName('hello\u200Bworld')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\u00ADworld')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\u2060world')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\u061Cworld')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\u180Eworld')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('\u202Etxt.exe')).toEqual({ value: 'txt.exe', error: null })
    expect(sanitizeName('\u2066hello\u2069world')).toEqual({ value: 'helloworld', error: null })
  })

  // Kept, although they are invisible too: removing one rewrites the text
  // rather than tidying it.
  it('keeps the joiners and the variation selectors', () => {
    expect(sanitizeName('\u{1F468}\u200D\u{1F469}').value).toBe('\u{1F468}\u200D\u{1F469}')
    expect(sanitizeName('\u0915\u094D\u200C\u0937').value).toBe('\u0915\u094D\u200C\u0937')
    expect(sanitizeName('love \u2764\uFE0F').value).toBe('love \u2764\uFE0F')
  })

  // A JavaScript string can hold an unpaired surrogate; a UTF-8 string cannot
  // represent one, so `TextEncoder` would count a U+FFFD the hub never
  // receives. The Go copy drops an invalid byte for the same reason, and the
  // shared fixture cannot carry either case -- `encoding/json` rewrites an
  // unpaired escape to U+FFFD before Go ever sees it.
  it('drops a lone surrogate', () => {
    expect(sanitizeName('Pl\uD800an')).toEqual({ value: 'Plan', error: null })
    expect(sanitizeName('Pl\uDC00an')).toEqual({ value: 'Plan', error: null })
    expect(sanitizeName('\uD800').error).not.toBeNull()
    // A well-formed pair is not a lone surrogate and must survive whole.
    expect(sanitizeName('hi \u{1F600}').value).toBe('hi \u{1F600}')
  })

  it('preserves allowed special characters', () => {
    expect(sanitizeName('name@here!')).toEqual({ value: 'name@here!', error: null })
    expect(sanitizeName('café')).toEqual({ value: 'café', error: null })
  })

  it('returns error for empty strings', () => {
    expect(sanitizeName('').error).not.toBeNull()
  })

  it('returns error for whitespace-only strings', () => {
    expect(sanitizeName('   ').error).not.toBeNull()
  })

  it('returns error for names exceeding 128 bytes', () => {
    expect(sanitizeName('a'.repeat(129)).error).not.toBeNull()
  })

  // The hub measures with Go's `len`, which counts BYTES. A character count
  // accepted 128 CJK characters here and the hub then refused all 384 bytes
  // of them, with the refusal arriving as a failed write and no explanation
  // at the field.
  it('counts UTF-8 bytes, not UTF-16 units', () => {
    expect(sanitizeName('\u4E00'.repeat(42)).error).toBeNull()
    expect(sanitizeName('\u4E00'.repeat(43)).error).not.toBeNull()
    expect(sanitizeName('\u4E00'.repeat(45)).error).not.toBeNull()
  })

  // The hub strips every rune `unicode.IsControl` reports, and the Unicode
  // Cc category covers U+0080-U+009F as well as U+0000-U+001F. A name that
  // carries one is not a name the hub stores unchanged, so it must not pass
  // here either.
  it('strips the C1 control block', () => {
    expect(sanitizeName('hello\u009Fworld')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\u0084world')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('\u009F').error).not.toBeNull()
  })

  // Every code point in the two control blocks, rather than one representative
  // per block.
  //
  // `NAME_INVISIBLE_G` encodes "control minus whitespace" as two hand-punched
  // holes in a literal range list (`\x00-\x08\x0E-\x1F` and
  // `\x7F-\x84\x86-\x9F`), which is the COMPLEMENT of `NAME_WHITESPACE_G`
  // transcribed by hand. Narrow the fold class and a hole becomes a leak: the
  // character reaches the stored name, and the hub then refuses it with no
  // explanation at the field. U+000B and U+000C had no case in either suite
  // before this test, and they are the two the holes cover by construction and
  // nothing else covered by example.
  it('leaves no C0 or C1 control in the result', () => {
    for (let cp = 0; cp <= 0x9F; cp++) {
      if (cp >= 0x20 && cp < 0x7F)
        continue // printable ASCII, which the rule keeps
      const ch = String.fromCodePoint(cp)
      const got = cleanName(`a${ch}b`)
      const label = `U+${cp.toString(16).toUpperCase().padStart(4, '0')}`
      // Either the character vanished, or it folded to one space. Nothing else
      // is a correct answer.
      expect(['ab', 'a b'], `cleanName must strip or fold ${label}, got ${JSON.stringify(got)}`)
        .toContain(got)
      expect(got, `${label} survived into the result`).not.toContain(ch)
    }
  })

  // The mirror: every printable ASCII character survives, so the loop above
  // cannot pass by stripping too much.
  it('keeps every printable ASCII character', () => {
    for (let cp = 0x20; cp < 0x7F; cp++) {
      const ch = String.fromCodePoint(cp)
      const want = cp === 0x20 ? 'a b' : `a${ch}b`
      expect(cleanName(`a${ch}b`), `U+${cp.toString(16)}`).toBe(want)
    }
  })

  // U+0085 is Cc AND whitespace, so it FOLDS rather than vanishing. Go's
  // `unicode.IsSpace` claims it and JavaScript's `\s` does not, which is why
  // this side spells it out in its own fold set. Get that wrong and the two
  // languages disagree on one character with nothing to say so.
  it('folds the next-line control rather than stripping it', () => {
    expect(sanitizeName('hello\u0085world')).toEqual({ value: 'hello world', error: null })
    expect(sanitizeName('\u0085').error).not.toBeNull()
  })

  // U+FEFF is category Cf, so Go's `unicode.IsControl` reports false for it.
  // It is stripped anyway: `String.prototype.trim` removes it and Go's
  // `strings.TrimSpace` keeps it, so a pasted byte order mark was the one input
  // on which this rule and its Go copy disagreed.
  it('strips the byte order mark', () => {
    expect(sanitizeName('\uFEFFhello\uFEFFworld')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('\uFEFF').error).not.toBeNull()
  })

  it('returns error when only stripped characters remain', () => {
    expect(sanitizeName('\x00\x01\x7F').error).not.toBeNull()
    expect(sanitizeName('\uFEFF\u200B\u00AD\u2060').error).not.toBeNull()
    expect(sanitizeName('\t\n\r  \u00A0').error).not.toBeNull()
  })
})

// The fallback branch runs the same rule, and a display name reaches
// users.display_name from the signup form, the profile form and the OAuth
// completion page — the last of which passes the name the provider reported.
describe('sanitizeDisplayName', () => {
  it('uses the display name when it is not empty', () => {
    expect(sanitizeDisplayName('Ada Lovelace', 'ada')).toEqual({ value: 'Ada Lovelace', error: null })
  })

  it('falls back only on the EMPTY string, not on one that cleans to empty', () => {
    expect(sanitizeDisplayName('', 'ada')).toEqual({ value: 'ada', error: null })
    // The `||` reads the RAW value, so a name of invisible characters is
    // non-empty there and takes the error rather than the fallback.
    expect(sanitizeDisplayName('\u200B\uFEFF', 'ada').error).not.toBeNull()
  })

  it('applies the relaxed rule to both branches', () => {
    expect(sanitizeDisplayName('Ada "100%" O$Brien', 'ada').value).toBe('Ada "100%" O$Brien')
    expect(sanitizeDisplayName('  Ada \t Lovelace  ', 'ada').value).toBe('Ada Lovelace')
    expect(sanitizeDisplayName('', '  Grace \n Hopper  ').value).toBe('Grace Hopper')
  })

  it('refuses an over-long name and an over-long fallback', () => {
    expect(sanitizeDisplayName('a'.repeat(NAME_BYTE_LIMIT + 1), 'ada').error).not.toBeNull()
    expect(sanitizeDisplayName('', 'a'.repeat(NAME_BYTE_LIMIT + 1)).error).not.toBeNull()
  })
})

describe('cleanName', () => {
  it('returns a name sanitizeName already accepts unchanged', () => {
    expect(cleanName('Refactor the parser')).toBe('Refactor the parser')
    expect(cleanName('a'.repeat(128))).toBe('a'.repeat(128))
  })

  // The case a character count misses: 50 CJK characters is 50 characters and
  // 150 BYTES. sanitizeName refuses it; cleanName cuts it to the 42 characters
  // that fit, which is what the worker stores.
  it('cuts to the byte limit instead of refusing', () => {
    const long = '一'.repeat(50)
    expect(sanitizeName(long).error).not.toBeNull()
    expect(cleanName(long)).toBe('一'.repeat(42))
  })

  // `slice(0, 128)` counts UTF-16 code units and would leave half a surrogate
  // pair, which the worker's protobuf decode then turns into U+FFFD.
  it('cuts on a character boundary, never inside a surrogate pair', () => {
    const cleaned = cleanName('a\u{1F600}'.repeat(30))
    expect(cleaned).toBe(`${'a\u{1F600}'.repeat(25)}a`)
    expect(cleaned).not.toContain('\uFFFD')
    expect(new TextEncoder().encode(cleaned).length).toBeLessThanOrEqual(128)
  })

  it('returns empty when nothing survives', () => {
    expect(cleanName('')).toBe('')
    expect(cleanName('   ')).toBe('')
    expect(cleanName('\x00\x01\x7F')).toBe('')
    expect(cleanName('\u200B'.repeat(200))).toBe('')
  })

  // The four characters this rule used to strip now reach the row. The old
  // rule emptied each of these, and a caller's fallback label replaced a title
  // the user typed.
  it('keeps a title made only of the characters the old rule stripped', () => {
    expect(cleanName('$$%%')).toBe('$$%%')
    expect(cleanName('npm test --grep "$FOO"')).toBe('npm test --grep "$FOO"')
    expect(cleanName('%'.repeat(200))).toBe('%'.repeat(128))
  })

  // The order the rule chose: CLEAN FIRST, CUT SECOND. A cut-first rule spent
  // its whole budget on characters it was about to remove, and returned the
  // empty string for a title that holds one.
  it('cleans before it cuts, so a long stripped prefix does not remove the title', () => {
    expect(cleanName(`${'\u200B'.repeat(130)}Plan`)).toBe('Plan')
    expect(cleanName(`${'\x00'.repeat(500)}Plan`)).toBe('Plan')
    expect(cleanName(`${' '.repeat(300)}Plan\nthe\tmigration`)).toBe('Plan the migration')
  })

  // The fold is what keeps two pasted lines apart. Stripping the newline
  // instead ran the last word of one line into the first of the next.
  it('folds whitespace instead of dropping it', () => {
    expect(cleanName('Fix parser\nAdd tests')).toBe('Fix parser Add tests')
    expect(cleanName('  Fix   parser \t\n Add\u00A0tests  ')).toBe('Fix parser Add tests')
  })

  // The property that lets the browser and the worker both apply the rule: the
  // title the user sees after a rename is the title the worker stores.
  it('is idempotent', () => {
    const inputs = [
      'hello',
      '  spaced  ',
      'a"b\\c$d%e',
      'Fix parser\nAdd tests',
      '  lots \t of \u00A0 whitespace  ',
      '一'.repeat(50),
      'a'.repeat(200),
      '%'.repeat(200),
      '\uFEFFPlan',
      '\u202EPlan\u200B',
      'Pl\uD800an',
      '',
    ]
    for (const input of inputs) {
      const once = cleanName(input)
      expect(cleanName(once)).toBe(once)
    }
  })

  it('returns a name sanitizeName accepts unchanged', () => {
    const inputs = [
      'a'.repeat(500),
      '一'.repeat(500),
      '\u{1F600}'.repeat(100),
      '%'.repeat(500),
      '\u200B'.repeat(200) + '$'.repeat(200),
      'Pl\uD800an'.repeat(50),
    ]
    for (const input of inputs) {
      const cleaned = cleanName(input)
      expect(sanitizeName(cleaned)).toEqual({ value: cleaned, error: null })
    }
  })
})

/**
 * One case of the shared title-cleaning fixture. `input` and `cleaned` are both
 * SPECS: a spec builds one string as `text` repeated `repeat` times followed by
 * `tail`.
 */
interface TitleConformanceSpec {
  text?: string
  repeat?: number
  tail?: string
}

interface TitleConformanceCase {
  input: TitleConformanceSpec
  cleaned: TitleConformanceSpec
  why: string
}

function buildTitle(spec: TitleConformanceSpec): string {
  return (spec.text ?? '').repeat(spec.repeat ?? 1) + (spec.tail ?? '')
}

/**
 * Resolved from this file rather than the CWD: vitest's working directory is
 * not part of the contract, and the fixture lives at the repo root (outside
 * `src`, so outside tsconfig's `include` and vite's root -- which is why this
 * is a runtime read rather than a static JSON import).
 */
const titleConformancePath = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../../testdata/title_cleaning_conformance.json',
)

describe('cleanName conformance', () => {
  const fixture = JSON.parse(readFileSync(titleConformancePath, 'utf8')) as {
    cases: TitleConformanceCase[]
  }

  // A fixture that silently loads zero cases would make this block pass while
  // asserting nothing -- the one failure mode a shared fixture must not have.
  it('loads the shared fixture', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$why', (c) => {
    const want = buildTitle(c.cleaned)
    expect(cleanName(buildTitle(c.input))).toBe(want)
    expect(cleanName(want), 'cleaning a cleaned title must change nothing').toBe(want)
  })
})

describe('validateEmail', () => {
  it('accepts empty string', () => {
    expect(validateEmail('')).toBeNull()
  })

  it('accepts valid emails', () => {
    expect(validateEmail('user@example.com')).toBeNull()
    expect(validateEmail('alice.bob@example.co.uk')).toBeNull()
    expect(validateEmail('user+tag@domain.org')).toBeNull()
    expect(validateEmail('a@b.co')).toBeNull()
  })

  it('rejects emails without @', () => {
    expect(validateEmail('userexample.com')).not.toBeNull()
  })

  it('rejects emails without dot in domain', () => {
    expect(validateEmail('user@localhost')).not.toBeNull()
  })

  it('rejects emails with spaces', () => {
    expect(validateEmail('user @example.com')).not.toBeNull()
  })

  it('rejects emails with angle brackets', () => {
    expect(validateEmail('<user@example.com>')).not.toBeNull()
  })

  it('rejects display name format', () => {
    expect(validateEmail('Alice <alice@example.com>')).not.toBeNull()
  })

  it('rejects emails exceeding 254 characters', () => {
    expect(validateEmail(`${'a'.repeat(250)}@b.co`)).not.toBeNull()
  })
})

describe('validatePassword', () => {
  it('rejects empty password', () => {
    expect(validatePassword('')).not.toBeNull()
  })

  it('rejects password shorter than 8 characters', () => {
    expect(validatePassword('1234567')).not.toBeNull()
  })

  it('accepts password at minimum length (8 chars)', () => {
    expect(validatePassword('12345678')).toBeNull()
  })

  it('accepts typical password', () => {
    expect(validatePassword('my-secure-password')).toBeNull()
  })

  it('accepts password at maximum length (128 chars)', () => {
    expect(validatePassword('a'.repeat(128))).toBeNull()
  })

  it('rejects password exceeding 128 characters', () => {
    expect(validatePassword('a'.repeat(129))).not.toBeNull()
  })

  it('rejects a non-ASCII password whatever its length', () => {
    expect(validatePassword('p\u00E4ssw\u00F6rd')).not.toBeNull()
    expect(validatePassword('\u4E2D'.repeat(43))).not.toBeNull()
  })

  // The control block and DEL are ASCII, and the rule refuses them anyway:
  // such a character reaches a password field through a paste accident or a
  // terminal control sequence, never through deliberate typing.
  it('rejects an unprintable ASCII character', () => {
    for (const code of [0x00, 0x09, 0x0A, 0x0D, 0x1F, 0x7F]) {
      expect(validatePassword(`passwor${String.fromCharCode(code)}`), `code unit ${code}`)
        .toContain('printable ASCII')
    }
  })

  // The space is printable, so a passphrase keeps its spaces. Neither side
  // trims a password, so a leading space and a trailing space survive too.
  it('accepts a space anywhere in the password', () => {
    expect(validatePassword('correct horse battery staple')).toBeNull()
    expect(validatePassword(' password ')).toBeNull()
    expect(validatePassword(' '.repeat(8))).toBeNull()
  })

  // Both edges of the range at once: 0x1F and 0x7F are refused, 0x20 and
  // 0x7E are accepted, and no code unit above 0x7E passes. Each probe is 8
  // code units long, so only the character-set rule can refuse one.
  it('accepts exactly the printable ASCII code units', () => {
    for (let code = 0; code <= 0xFF; code++) {
      const error = validatePassword(`passwor${String.fromCharCode(code)}`)
      const label = `code unit 0x${code.toString(16)}`
      if (code >= 0x20 && code <= 0x7E) {
        expect(error, label).toBeNull()
      }
      else {
        expect(error, label).toContain('printable ASCII')
      }
    }
  })

  // The character set is the actionable complaint when a password breaks
  // both rules: a user who counted 3 characters cannot act on a minimum of
  // 8 that the hub measured in bytes.
  it('reports the character set before the length', () => {
    expect(validatePassword('\u4E2D'.repeat(3))).toContain('printable ASCII')
    expect(validatePassword('\u4E2D'.repeat(200))).toContain('printable ASCII')
    expect(validatePassword(`ab${String.fromCharCode(0x01)}`)).toContain('printable ASCII')
    expect(validatePassword(`a${String.fromCharCode(0x07)}`.repeat(100))).toContain('printable ASCII')
  })

  // The property the character-set rule exists for: an accepted password
  // holds one UTF-16 code unit for each UTF-8 byte, so this limit and the
  // hub's are the same limit.
  it('accepts only passwords whose code units and bytes agree', () => {
    for (const password of [
      'a'.repeat(8),
      'a'.repeat(128),
      '~ !@#$%^&*()_+',
      'my-secure-password',
      'correct horse battery staple',
      ' password ',
    ]) {
      expect(validatePassword(password)).toBeNull()
      expect(new TextEncoder().encode(password).length).toBe(password.length)
    }
  })
})

/**
 * A case from the cross-language conformance fixture. See that file's
 * `_readme` for the contract; the short version is that the password is
 * `password` repeated `repeat` times, both validators accept it iff `valid`,
 * and a refusal reports the rule named by `refusal`. This suite asserts the
 * browser's half; backend/util/validate/password_test.go asserts the hub's.
 */
interface PasswordConformanceCase {
  password: string
  repeat?: number
  valid: boolean
  refusal: string
  why: string
}

/**
 * The substring this module's message carries for each fixture refusal
 * token. The Go suite holds the same map against its own wording, because
 * the two messages differ by the leading capital only.
 */
const PASSWORD_REFUSAL_MARKERS: Record<string, string> = {
  too_short: 'at least',
  too_long: 'at most',
  not_printable_ascii: 'printable ASCII',
}

/**
 * Resolved from this file rather than the CWD: vitest's working directory is
 * not part of the contract, and the fixture lives at the repo root (outside
 * `src`, so outside tsconfig's `include` and vite's root -- which is why this
 * is a runtime read rather than a static JSON import).
 */
const passwordConformancePath = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../../testdata/password_policy_conformance.json',
)

describe('validatePassword conformance', () => {
  const fixture = JSON.parse(readFileSync(passwordConformancePath, 'utf8')) as {
    cases: PasswordConformanceCase[]
  }

  // A fixture that silently loads zero cases would make this block pass
  // while asserting nothing -- the one failure mode a shared fixture must
  // not have.
  it('loads the shared fixture', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$why', (c) => {
    const password = c.password.repeat(c.repeat ?? 1)
    const error = validatePassword(password)
    if (c.valid) {
      expect(c.refusal).toBe('')
      expect(error).toBeNull()
      return
    }
    expect(error).not.toBeNull()
    const marker = PASSWORD_REFUSAL_MARKERS[c.refusal]
    expect(marker, `unknown refusal token "${c.refusal}"`).toBeDefined()
    expect(error).toContain(marker)
  })
})

describe('stripRemotePrefix', () => {
  it('returns bare local names unchanged', () => {
    expect(stripRemotePrefix('main')).toBe('main')
    expect(stripRemotePrefix('feature-branch')).toBe('feature-branch')
  })

  it('strips a single remote prefix', () => {
    expect(stripRemotePrefix('origin/main')).toBe('main')
    expect(stripRemotePrefix('upstream/release')).toBe('release')
  })

  it('only strips the first slash-delimited segment, leaving deeper slashes intact', () => {
    // The worker maps `origin/feature/foo` to the local branch
    // `feature/foo`, so the helper must drop only the first segment.
    expect(stripRemotePrefix('origin/feature/foo')).toBe('feature/foo')
    expect(stripRemotePrefix('origin/release/v1/rc1')).toBe('release/v1/rc1')
  })

  it('returns empty string unchanged', () => {
    expect(stripRemotePrefix('')).toBe('')
  })

  it('treats a leading slash as a remote with empty name', () => {
    // Not a valid ref, but the helper should not crash; it returns
    // everything after the first slash.
    expect(stripRemotePrefix('/main')).toBe('main')
  })

  it('returns empty string when input is just a slash', () => {
    expect(stripRemotePrefix('/')).toBe('')
  })
})

describe('validateReservedUsername', () => {
  it('rejects "solo" in every context', () => {
    expect(validateReservedUsername('solo', false)).not.toBeNull()
    expect(validateReservedUsername('solo', true)).not.toBeNull()
    expect(validateReservedUsername('SOLO', true)).not.toBeNull()
    expect(validateReservedUsername('  solo  ', true)).not.toBeNull()
  })

  it('rejects "admin" only when allowAdmin is false', () => {
    expect(validateReservedUsername('admin', false)).not.toBeNull()
    expect(validateReservedUsername('ADMIN', false)).not.toBeNull()
    expect(validateReservedUsername('admin', true)).toBeNull()
  })

  it('accepts ordinary usernames in both contexts', () => {
    expect(validateReservedUsername('alice', false)).toBeNull()
    expect(validateReservedUsername('alice', true)).toBeNull()
    expect(validateReservedUsername('admin-dev', false)).toBeNull()
  })
})

describe('validateBranchName', () => {
  describe('valid branch names', () => {
    const validNames = [
      'feature-branch',
      'fix/login-bug',
      'v1.0.0',
      'my_branch',
      'a',
      'feature/deep/nesting',
      'UPPERCASE',
      'mixed-Case_123',
      'release/2024.01',
    ]

    for (const name of validNames) {
      it(`accepts "${name}"`, () => {
        expect(validateBranchName(name)).toBeNull()
        expect(isValidBranchName(name)).toBe(true)
      })
    }
  })

  describe('empty and too long', () => {
    it('rejects empty string', () => {
      expect(validateBranchName('')).toBe('Branch name must not be empty')
      expect(isValidBranchName('')).toBe(false)
    })

    it('rejects string longer than 256 bytes', () => {
      const longName = 'a'.repeat(257)
      expect(validateBranchName(longName)).toBe('Branch name must be at most 256 bytes')
    })

    it('accepts string of exactly 256 bytes', () => {
      const name = 'a'.repeat(256)
      expect(validateBranchName(name)).toBeNull()
    })

    // The unit is BYTES, because the worker's `gitutil.ValidateBranchName`
    // measures with Go's `len`. 86 CJK characters is 86 UTF-16 code units and
    // 258 bytes, so a `String.length` count offered a branch that the worker
    // then refused, with a message that said "characters" on both sides and
    // was true on neither.
    it('counts UTF-8 bytes and not UTF-16 code units', () => {
      expect(validateBranchName('一'.repeat(85))).toBeNull()
      expect(validateBranchName('一'.repeat(86))).toBe('Branch name must be at most 256 bytes')
    })
  })

  // The worker refuses the same characters. A name the panel offers and the
  // worker then refuses shows the user two answers for one name.
  describe('agrees with the worker copy', () => {
    it('rejects the shell metacharacters $ and %', () => {
      expect(validateBranchName('feat/$HOME')).toBe('Branch name contains invalid characters')
      expect(validateBranchName('feat/100%')).toBe('Branch name contains invalid characters')
    })

    // Go's `unicode.IsControl` reports the whole Cc category, which is BOTH
    // U+0000-U+001F and U+007F-U+009F. A class that stopped at U+007F let a
    // name through here that the worker then refused.
    it('rejects the C1 control block, not the C0 block alone', () => {
      expect(validateBranchName('feat/ab')).toBe('Branch name contains invalid characters')
      expect(validateBranchName('feat/ab')).toBe('Branch name contains invalid characters')
      expect(validateBranchName('feat/ab')).toBe('Branch name contains invalid characters')
    })
  })

  describe('forbidden characters', () => {
    const forbidden: [string, string][] = [
      ['foo bar', 'space'],
      ['foo~bar', 'tilde'],
      ['foo^bar', 'caret'],
      ['foo:bar', 'colon'],
      ['foo?bar', 'question mark'],
      ['foo*bar', 'asterisk'],
      ['foo[bar', 'open bracket'],
      ['foo]bar', 'close bracket'],
      ['foo\\bar', 'backslash'],
    ]

    for (const [name, desc] of forbidden) {
      it(`rejects "${name}" (contains ${desc})`, () => {
        expect(validateBranchName(name)).toBe('Branch name contains invalid characters')
      })
    }
  })

  describe('control characters', () => {
    it('rejects null byte', () => {
      expect(validateBranchName('foo\x00bar')).toBe('Branch name contains invalid characters')
    })

    it('rejects newline', () => {
      expect(validateBranchName('foo\nbar')).toBe('Branch name contains invalid characters')
    })

    it('rejects tab', () => {
      expect(validateBranchName('foo\tbar')).toBe('Branch name contains invalid characters')
    })

    it('rejects DEL (0x7F)', () => {
      expect(validateBranchName('foo\x7Fbar')).toBe('Branch name contains invalid characters')
    })
  })

  describe('forbidden leading characters', () => {
    it('rejects leading dot', () => {
      expect(validateBranchName('.foo')).toBe('Branch name must not start with /, ., -, or @')
    })

    it('rejects leading dash', () => {
      expect(validateBranchName('-foo')).toBe('Branch name must not start with /, ., -, or @')
    })

    it('rejects leading slash', () => {
      expect(validateBranchName('/foo')).toBe('Branch name must not start with /, ., -, or @')
    })

    it('rejects leading @', () => {
      expect(validateBranchName('@foo')).toBe('Branch name must not start with /, ., -, or @')
    })
  })

  describe('forbidden trailing patterns', () => {
    it('rejects trailing slash', () => {
      expect(validateBranchName('foo/')).toBe('Branch name must not end with /, ., or .lock')
    })

    it('rejects trailing dot', () => {
      expect(validateBranchName('foo.')).toBe('Branch name must not end with /, ., or .lock')
    })

    it('rejects trailing .lock', () => {
      expect(validateBranchName('foo.lock')).toBe('Branch name must not end with /, ., or .lock')
    })
  })

  describe('forbidden sequences', () => {
    it('rejects double dot (..)', () => {
      expect(validateBranchName('foo..bar')).toBe('Branch name must not contain ..')
    })

    it('rejects double slash (//)', () => {
      expect(validateBranchName('foo//bar')).toBe('Branch name must not contain //')
    })

    it('rejects slash-dot (/.)', () => {
      expect(validateBranchName('foo/.bar')).toBe('Branch name must not contain /.')
    })
  })
})

describe('sanitizeSlug', () => {
  describe('valid slugs', () => {
    const cases: [string, string][] = [
      ['a', 'a'],
      ['myname', 'myname'],
      ['user123', 'user123'],
      ['my-name', 'my-name'],
      ['a-b-c', 'a-b-c'],
      ['a'.repeat(32), 'a'.repeat(32)],
    ]

    for (const [input, expected] of cases) {
      it(`accepts "${input}" → "${expected}"`, () => {
        const [slug, err] = sanitizeSlug('test', input)
        expect(err).toBeNull()
        expect(slug).toBe(expected)
      })
    }
  })

  describe('trimming and lowercasing', () => {
    it('lowercases uppercase input', () => {
      const [slug, err] = sanitizeSlug('test', 'MyName')
      expect(err).toBeNull()
      expect(slug).toBe('myname')
    })

    it('trims leading spaces', () => {
      const [slug, err] = sanitizeSlug('test', '  hello')
      expect(err).toBeNull()
      expect(slug).toBe('hello')
    })

    it('trims trailing spaces', () => {
      const [slug, err] = sanitizeSlug('test', 'hello  ')
      expect(err).toBeNull()
      expect(slug).toBe('hello')
    })

    it('trims and lowercases', () => {
      const [slug, err] = sanitizeSlug('test', '  Hello  ')
      expect(err).toBeNull()
      expect(slug).toBe('hello')
    })

    it('lowercases with hyphens and numbers', () => {
      const [slug, err] = sanitizeSlug('test', 'My-Org-123')
      expect(err).toBeNull()
      expect(slug).toBe('my-org-123')
    })
  })

  describe('empty and length', () => {
    it('rejects empty string', () => {
      const [slug, err] = sanitizeSlug('Username', '')
      expect(err).toBe('Username must not be empty')
      expect(slug).toBe('')
    })

    it('rejects whitespace only', () => {
      const [slug, err] = sanitizeSlug('Username', '   ')
      expect(err).toBe('Username must not be empty')
      expect(slug).toBe('')
    })

    it('rejects string longer than 32 characters', () => {
      const [slug, err] = sanitizeSlug('Username', 'a'.repeat(33))
      expect(err).toBe('Username must be at most 32 characters')
      expect(slug).toBe('')
    })

    it('accepts exactly 32 characters', () => {
      const [slug, err] = sanitizeSlug('test', 'a'.repeat(32))
      expect(err).toBeNull()
      expect(slug).toBe('a'.repeat(32))
    })
  })

  describe('invalid characters', () => {
    const cases: [string, string][] = [
      ['my name', 'space in middle'],
      ['my_name', 'underscore'],
      ['my.name', 'dot'],
      ['user@org', 'at sign'],
      ['user/org', 'slash'],
      ['café', 'unicode'],
    ]

    for (const [input, desc] of cases) {
      it(`rejects "${input}" (${desc})`, () => {
        const [slug, err] = sanitizeSlug('test', input)
        expect(err).toBe('test must contain only letters, numbers, and hyphens')
        expect(slug).toBe('')
      })
    }
  })

  describe('structural rules', () => {
    it('rejects leading hyphen', () => {
      const [slug, err] = sanitizeSlug('test', '-myname')
      expect(err).toBe('test must not start with a hyphen')
      expect(slug).toBe('')
    })

    it('rejects trailing hyphen', () => {
      const [slug, err] = sanitizeSlug('test', 'myname-')
      expect(err).toBe('test must not end with a hyphen')
      expect(slug).toBe('')
    })

    it('rejects consecutive hyphens', () => {
      const [slug, err] = sanitizeSlug('test', 'my--name')
      expect(err).toBe('test must not contain consecutive hyphens')
      expect(slug).toBe('')
    })

    it('rejects triple hyphens', () => {
      const [slug, err] = sanitizeSlug('test', 'my---name')
      expect(err).toBe('test must not contain consecutive hyphens')
      expect(slug).toBe('')
    })
  })

  describe('field name in error messages', () => {
    it('includes "Username" in error', () => {
      const [, err] = sanitizeSlug('Username', '')
      expect(err).toContain('Username')
    })

    it('includes "Username" in error for invalid slug', () => {
      const [, err] = sanitizeSlug('Username', 'bad_slug')
      expect(err).toContain('Username')
    })
  })
})

describe('validateSessionId', () => {
  it('accepts the empty value, which means no resume', () => {
    expect(validateSessionId('')).toBeNull()
  })

  it('accepts the shapes the providers emit', () => {
    expect(validateSessionId('abc-123')).toBeNull()
    expect(validateSessionId('session_456')).toBeNull()
    expect(validateSessionId('thread-uuid-v4-compat')).toBeNull()
  })

  it('refuses a control character', () => {
    for (const id of ['has\x00nul', 'has\x1Funitsep', 'has\x7Fdel', 'has\nnewline', 'has\rcarriage'])
      expect(validateSessionId(id), id).not.toBeNull()
  })

  // U+FEFF is in this class and NOT in the C0/C1 blocks, so a regex that
  // covers only the control blocks would let it through. It travels invisibly
  // through a copy and paste, and a token that carries one resumes nothing.
  it('refuses a byte order mark', () => {
    expect(validateSessionId('\uFEFFabc-123')).not.toBeNull()
    expect(validateSessionId('abc\uFEFF123')).not.toBeNull()
  })

  // Every invisible format character is refused, and not U+FEFF alone. Each
  // one travels through a copy and a paste unseen, and a token that carries
  // one names no session the agent knows: the resume then starts a fresh
  // conversation with no report of why.
  it('refuses every invisible format character', () => {
    const invisible = [
      0x00AD,
      0x061C,
      0x180E,
      0x200B,
      0x200E,
      0x200F,
      0x202A,
      0x202B,
      0x202C,
      0x202D,
      0x202E,
      0x2060,
      0x2066,
      0x2067,
      0x2068,
      0x2069,
      0xFEFF,
    ]
    for (const cp of invisible) {
      const ch = String.fromCodePoint(cp)
      const label = `U+${cp.toString(16).toUpperCase().padStart(4, '0')}`
      expect(validateSessionId(`abc${ch}123`), label).not.toBeNull()
      expect(validateSessionId(`${ch}abc-123`), `leading ${label}`).not.toBeNull()
    }
  })

  // The three the NAME rule keeps deliberately. The token class repeats the
  // name rule's list, so it must repeat the exclusions too \u2014 refusing U+200D
  // here would be a one-sided narrowing nothing else in the repo asked for.
  it('accepts the invisible characters the name rule keeps', () => {
    for (const cp of [0x200C, 0x200D, 0xFE0F]) {
      const ch = String.fromCodePoint(cp)
      expect(validateSessionId(`abc${ch}123`), `U+${cp.toString(16)}`).toBeNull()
    }
  })

  // `TextEncoder` rewrites an unpaired surrogate to U+FFFD, so without this
  // the browser measured and then sent a token the user never typed, and the
  // hub read the U+FFFD as ordinary text and resumed nothing. The Go copy
  // refuses an invalid byte for the same reason. The shared fixture cannot
  // carry this case, because `JSON.parse` and `encoding/json` both rewrite an
  // unpaired escape before either suite sees it.
  it('refuses a lone surrogate', () => {
    expect(validateSessionId('abc\uD800def')).not.toBeNull()
    expect(validateSessionId('abc\uDC00def')).not.toBeNull()
    expect(validateSessionId('\uD800')).not.toBeNull()
    // A well-formed pair is ordinary text and passes.
    expect(validateSessionId('abc\u{1F600}def')).toBeNull()
  })

  // `LONE_SURROGATE_G` carries the `g` flag for its use in `replace`, and
  // `RegExp.prototype.test` on a global regex advances `lastIndex` and answers
  // from there on the NEXT call. Without a reset, the SECOND call on the same
  // bad value returns null and the token goes through.
  it('refuses a lone surrogate on every call, not only the first', () => {
    for (let i = 0; i < 5; i++)
      expect(validateSessionId('abc\uD800def'), `call ${i}`).not.toBeNull()
  })

  // The guard on the decoupling. This rule used to be defined as "sanitizeName
  // leaves the value unchanged", so relaxing what a NAME may hold would have
  // silently widened what a TOKEN may hold. A session ID becomes an argv
  // element of `claude --resume <id>` and the `sessionId` member of an ACP
  // request, so it must keep refusing what it refused.
  it('stays narrower than the name rule', () => {
    for (const id of ['has"quote', 'has\\backslash', 'has$dollar', 'has%percent']) {
      expect(sanitizeName(id), `the name rule must accept ${id} for this case to bite`)
        .toEqual({ value: id, error: null })
      expect(validateSessionId(id), id).not.toBeNull()
    }
  })

  // The name rule TRIMS and FOLDS, so it would have accepted these after a
  // rewrite. A rewritten token resumes a different session, or no session.
  it('refuses whitespace at either end', () => {
    expect(validateSessionId(' abc-123')).not.toBeNull()
    expect(validateSessionId('abc-123 ')).not.toBeNull()
    expect(validateSessionId('   ')).not.toBeNull()
  })

  // The limit counts UTF-8 BYTES, because the hub's `len` does. 43 CJK
  // characters is 43 characters and 129 bytes.
  it('counts UTF-8 bytes, not UTF-16 units', () => {
    expect(validateSessionId('a'.repeat(SESSION_ID_BYTE_LIMIT))).toBeNull()
    expect(validateSessionId('a'.repeat(SESSION_ID_BYTE_LIMIT + 1))).not.toBeNull()
    expect(validateSessionId('\u4E00'.repeat(42))).toBeNull()
    expect(validateSessionId('\u4E00'.repeat(43))).not.toBeNull()
  })

  // An interior space is accepted and is NOT folded. The name rule folds a run
  // of whitespace to one space; a token must not, because the fold resumes a
  // different session.
  it('accepts interior whitespace without folding it', () => {
    expect(validateSessionId('a b')).toBeNull()
    expect(validateSessionId('a  b')).toBeNull()
  })

  // The ORDER of the tests is part of the rule, and this is what pins it on
  // this side. `String.prototype.trim` removes U+FEFF and Go's
  // `strings.TrimSpace` does not, while Go's claims U+0085 and `trim` does
  // not \u2014 so a rule that trimmed FIRST reported "must not start or end with
  // whitespace" in one language and "contains invalid characters" in the
  // other, for one input.
  it('reports the character rule and not the whitespace rule at an edge', () => {
    for (const id of ['\uFEFFabc-123', 'abc-123\uFEFF', '\u0085abc-123', 'abc-123\u0085'])
      expect(validateSessionId(id), id).toBe('Session ID contains invalid characters')
  })
})

/**
 * The pinned fold set against the runtime's own `\s`.
 *
 * `NAME_WHITESPACE_CLASS` is spelled out by code point so a JavaScript engine
 * upgrade cannot move the fold set out from under the Go copy. The pin costs
 * staleness: a Space_Separator added to Unicode later is NOT folded, and
 * renders as a visible character inside a title, until somebody adds it. This
 * block is what makes that a decision rather than an accident.
 *
 * A failure here is NOT automatically a bug. It means the engine's table
 * moved, and a human must decide whether the name rule adopts the new
 * character -- in BOTH languages, together with a case in the shared fixture.
 * The Go half is `TestNameWhitespaceMatchesUnicode`.
 */
describe('pinned whitespace set', () => {
  // Go's `unicode.IsSpace` is `\s` with exactly two edits: U+0085 is IN,
  // because Go claims it and `\s` does not, and U+FEFF is OUT, because `\s`
  // claims it and Go does not.
  const GO_ONLY = 0x0085
  const JS_ONLY = 0xFEFF
  // Hoisted: the loops below run over the whole code-point space, and a
  // regex rebuilt per iteration would compile 1.1M times.
  const PINNED = new RegExp(`^[${NAME_WHITESPACE_CLASS}]$`, 'u')

  it('matches the engine\'s whitespace set apart from U+0085 and U+FEFF', () => {
    const extra: string[] = []
    const missing: string[] = []
    for (let cp = 0; cp <= 0x10FFFF; cp++) {
      if (cp >= 0xD800 && cp <= 0xDFFF)
        continue // a surrogate is not a character
      const ch = String.fromCodePoint(cp)
      const pinned = PINNED.test(ch)
      const want = cp === GO_ONLY ? true : cp === JS_ONLY ? false : /\s/u.test(ch)
      const label = `U+${cp.toString(16).toUpperCase().padStart(4, '0')}`
      if (pinned && !want)
        extra.push(label)
      else if (!pinned && want)
        missing.push(label)
    }
    expect(extra, 'the pinned fold set holds characters the engine no longer calls whitespace').toEqual([])
    expect(missing, 'the engine calls these whitespace and the pinned fold set does not hold them').toEqual([])
  })

  // A character cannot be both folded and stripped. `cleanNameChars` runs the
  // strip BEFORE the fold on this side and the Go copy asks the fold first, so
  // an overlap would give the two languages different answers for it.
  it('shares no character with the invisible-strip set', () => {
    for (let cp = 0; cp <= 0xFFFF; cp++) {
      if (cp >= 0xD800 && cp <= 0xDFFF)
        continue
      const ch = String.fromCodePoint(cp)
      const folded = PINNED.test(ch)
      if (!folded)
        continue
      expect(cleanName(`a${ch}b`), `U+${cp.toString(16)} is folded, so it must not also be stripped`).toBe('a b')
    }
  })
})

/**
 * The browser half of `testdata/session_id_conformance.json`. The Go suite
 * (`TestValidateSessionIDConformance`) reads the same file, so a one-sided
 * edit to either implementation turns this block red. See the file's own
 * `_readme` for the contract and for why the lone-surrogate case cannot live
 * there.
 */
describe('validateSessionId conformance', () => {
  interface SessionIdConformanceSpec {
    text?: string
    repeat?: number
    tail?: string
  }

  interface SessionIdConformanceCase {
    input: SessionIdConformanceSpec
    valid: boolean
    refusal: string
    why: string
  }

  function buildSessionId(spec: SessionIdConformanceSpec): string {
    return (spec.text ?? '').repeat(spec.repeat ?? 1) + (spec.tail ?? '')
  }

  // Each token maps to a substring of THIS side's message. The two languages'
  // messages differ by the leading capital only, so each suite carries its own
  // map and the fixture stays language-neutral.
  const refusalMarkers: Record<string, string> = {
    too_long: 'must be at most',
    not_utf8: 'must be valid UTF-8',
    forbidden_character: 'contains invalid characters',
    whitespace_at_edge: 'must not start or end with whitespace',
  }

  const sessionIdConformancePath = resolve(
    dirname(fileURLToPath(import.meta.url)),
    '../../../testdata/session_id_conformance.json',
  )

  const fixture = JSON.parse(readFileSync(sessionIdConformancePath, 'utf8')) as {
    cases: SessionIdConformanceCase[]
  }

  // A fixture that silently loads zero cases would make this block pass while
  // asserting nothing -- the one failure mode a shared fixture must not have.
  it('loads the shared fixture', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('$why', (c) => {
    const got = validateSessionId(buildSessionId(c.input))
    if (c.valid) {
      expect(c.refusal, `case "${c.why}" is valid, so its refusal must be empty`).toBe('')
      expect(got).toBeNull()
      return
    }
    expect(got, `case "${c.why}" must be refused`).not.toBeNull()
    const marker = refusalMarkers[c.refusal]
    expect(marker, `case "${c.why}" carries an unknown refusal token "${c.refusal}"`).toBeDefined()
    expect(got).toContain(marker)
  })
})
