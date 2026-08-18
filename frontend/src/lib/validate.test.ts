import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import {
  cleanName,
  isValidBranchName,
  sanitizeName,
  sanitizeSlug,
  stripRemotePrefix,
  validateBranchName,
  validateEmail,
  validatePassword,
  validateReservedUsername,
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

  it('strips forbidden characters and returns sanitized value', () => {
    expect(sanitizeName('name"quoted')).toEqual({ value: 'namequoted', error: null })
    expect(sanitizeName('back\\slash')).toEqual({ value: 'backslash', error: null })
    expect(sanitizeName('hello\tworld')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\nworld')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\x00world')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\x7Fworld')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello$world')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('100%done')).toEqual({ value: '100done', error: null })
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
    expect(sanitizeName('hello\u0085world')).toEqual({ value: 'helloworld', error: null })
    expect(sanitizeName('hello\u009Fworld')).toEqual({ value: 'helloworld', error: null })
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

  it('returns error when only forbidden characters remain', () => {
    expect(sanitizeName('"\\\t\n').error).not.toBeNull()
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
    expect(cleanName('$$%%')).toBe('')
    expect(cleanName('%'.repeat(200))).toBe('')
  })

  // The property that lets the browser and the worker both apply the rule: the
  // title the user sees after a rename is the title the worker stores.
  it('is idempotent', () => {
    for (const input of ['hello', '  spaced  ', 'a"b\\c$d%e', '一'.repeat(50), 'a'.repeat(200), '']) {
      const once = cleanName(input)
      expect(cleanName(once)).toBe(once)
    }
  })

  it('returns a name sanitizeName accepts unchanged', () => {
    for (const input of ['a'.repeat(500), '一'.repeat(500), '\u{1F600}'.repeat(100)]) {
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

    it('rejects string longer than 256 characters', () => {
      const longName = 'a'.repeat(257)
      expect(validateBranchName(longName)).toBe('Branch name must be at most 256 characters')
    })

    it('accepts string of exactly 256 characters', () => {
      const name = 'a'.repeat(256)
      expect(validateBranchName(name)).toBeNull()
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
