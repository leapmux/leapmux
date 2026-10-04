import { describe, expect, it } from 'vitest'
import { quotePosixShellArgument } from './shellArguments'

describe('quotePosixShellArgument', () => {
  it.each([
    { value: '', expected: '\'\'' },
    { value: 'one argument', expected: '\'one argument\'' },
    { value: 'one\'quote', expected: '\'one\'"\'"\'quote\'' },
    { value: '$(printf WRONG); `echo WRONG` *', expected: '\'$(printf WRONG); `echo WRONG` *\'' },
    { value: 'first\nsecond', expected: '\'first\nsecond\'' },
  ])('preserves the exact argument $value', ({ value, expected }) => {
    expect(quotePosixShellArgument(value)).toBe(expected)
  })
})
