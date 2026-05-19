import { describe, expect, it } from 'vitest'
import { formatErrorMessage } from '~/lib/errors'

describe('formatErrorMessage', () => {
  it('returns err.message when err is an Error', () => {
    expect(formatErrorMessage(new Error('boom'))).toBe('boom')
  })

  it('returns err.message even when a fallback is supplied (Error.message wins)', () => {
    // Regression guard: a dialog's fallback string must not mask the
    // server's actual error text.
    expect(formatErrorMessage(new Error('actual'), 'fallback')).toBe('actual')
  })

  it('returns the empty Error.message verbatim, not the fallback', () => {
    // A handler that throws an Error with no message is explicitly
    // choosing "no message" — preserving that distinguishes it from a
    // thrown literal.
    const empty = new Error('placeholder')
    empty.message = ''
    expect(formatErrorMessage(empty, 'fallback')).toBe('')
  })

  it('returns fallback for non-Error rejections when one is supplied', () => {
    expect(formatErrorMessage('plain string', 'Operation failed')).toBe('Operation failed')
    expect(formatErrorMessage(42, 'Operation failed')).toBe('Operation failed')
    expect(formatErrorMessage(null, 'Operation failed')).toBe('Operation failed')
    expect(formatErrorMessage(undefined, 'Operation failed')).toBe('Operation failed')
    expect(formatErrorMessage({ code: 'E_FOO' }, 'Operation failed')).toBe('Operation failed')
  })

  it('falls back to String(err) when no fallback is supplied (debug/log shape)', () => {
    expect(formatErrorMessage('plain string')).toBe('plain string')
    expect(formatErrorMessage(42)).toBe('42')
    expect(formatErrorMessage(null)).toBe('null')
    expect(formatErrorMessage(undefined)).toBe('undefined')
  })

  it('handles Error subclasses (TypeError, custom)', () => {
    class CustomError extends Error {}
    expect(formatErrorMessage(new TypeError('bad type'))).toBe('bad type')
    expect(formatErrorMessage(new CustomError('custom'))).toBe('custom')
  })

  it('treats fallback="" as a distinct choice from omitted fallback', () => {
    // An empty-string fallback means "show nothing"; the no-fallback
    // form means "show String(err)". Conflating them would silently
    // surface stringified objects in places that opted out.
    expect(formatErrorMessage({ x: 1 }, '')).toBe('')
    expect(formatErrorMessage({ x: 1 })).toBe('[object Object]')
  })
})
