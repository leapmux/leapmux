import { describe, expect, it } from 'vitest'
import { isValidBranchName, validateBranchName } from '~/lib/validate'

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
