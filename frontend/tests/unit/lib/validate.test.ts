import { describe, expect, it } from 'vitest'
import { isValidBranchName, sanitizeSlug, validateBranchName } from '~/lib/validate'

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
      ['caf\u00E9', 'unicode'],
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
