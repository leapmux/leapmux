import { describe, expect, it } from 'vitest'

import { sanitizeName, validateEmail, validatePassword, validateReservedUsername } from './validate'

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
