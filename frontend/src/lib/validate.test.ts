import { describe, expect, it } from 'vitest'

import {
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

  it('accepts names at max length (128 chars)', () => {
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

  it('returns error for names exceeding 128 characters', () => {
    expect(sanitizeName('a'.repeat(129)).error).not.toBeNull()
  })

  it('returns error when only forbidden characters remain', () => {
    expect(sanitizeName('"\\\t\n').error).not.toBeNull()
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

    it('includes "Organization name" in error', () => {
      const [, err] = sanitizeSlug('Organization name', 'bad_slug')
      expect(err).toContain('Organization name')
    })
  })
})
