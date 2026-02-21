import { describe, expect, it } from 'vitest'

import { isValidName, validateName } from './validate'

describe('validateName', () => {
  it('accepts valid simple names', () => {
    expect(validateName('hello')).toBeNull()
    expect(validateName('hello world')).toBeNull()
    expect(validateName('my-name')).toBeNull()
    expect(validateName('my_name')).toBeNull()
    expect(validateName('my.name')).toBeNull()
    expect(validateName('name123')).toBeNull()
    expect(validateName('My Name-1.0_beta')).toBeNull()
  })

  it('accepts names at max length (64 chars)', () => {
    expect(validateName('a'.repeat(64))).toBeNull()
  })

  it('trims whitespace before validation', () => {
    expect(validateName('  hello  ')).toBeNull()
  })

  it('rejects empty strings', () => {
    expect(validateName('')).not.toBeNull()
  })

  it('rejects whitespace-only strings', () => {
    expect(validateName('   ')).not.toBeNull()
  })

  it('rejects names exceeding 64 characters', () => {
    expect(validateName('a'.repeat(65))).not.toBeNull()
  })

  it('rejects special characters', () => {
    expect(validateName('name@here')).not.toBeNull()
    expect(validateName('hello!')).not.toBeNull()
    expect(validateName('path/name')).not.toBeNull()
    expect(validateName('back\\slash')).not.toBeNull()
    expect(validateName('name"quoted')).not.toBeNull()
  })

  it('rejects unicode characters', () => {
    expect(validateName('caf\u00E9')).not.toBeNull()
  })

  it('rejects emoji', () => {
    expect(validateName('hello\u{1F600}')).not.toBeNull()
  })

  it('rejects tabs and newlines', () => {
    expect(validateName('hello\tworld')).not.toBeNull()
    expect(validateName('hello\nworld')).not.toBeNull()
  })
})

describe('isValidName', () => {
  it('returns true for valid names', () => {
    expect(isValidName('hello')).toBe(true)
  })

  it('returns false for invalid names', () => {
    expect(isValidName('')).toBe(false)
    expect(isValidName('name@here')).toBe(false)
  })
})
