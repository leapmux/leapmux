import { describe, expect, it } from 'vitest'
import { requireNonemptyText } from './requiredText'

describe('requireNonemptyText', () => {
  it('accepts text that holds a character other than white space', () => {
    expect(() => requireNonemptyText(' a ', 'The test rule', 'field')).not.toThrow()
  })

  it.each(['', ' \n\t', undefined, null, 0, ['text']])('refuses %j with the subject and the field', (value) => {
    expect(() => requireNonemptyText(value, 'The test rule', 'field')).toThrow('The test rule requires nonempty text for field.')
  })
})
