import { describe, expect, it } from 'vitest'
import { cssAttributeValue } from './cssAttribute'

describe('cssAttributeValue', () => {
  it('keeps a value without a quote, a backslash, or a line break', () => {
    expect(cssAttributeValue('live-child-read')).toBe('live-child-read')
    expect(cssAttributeValue('')).toBe('')
  })

  it('escapes a quote and a backslash', () => {
    expect(cssAttributeValue('a"b\\c')).toBe('a\\"b\\\\c')
  })

  it.each([
    { value: 'a\nb', escaped: 'a\\a b' },
    { value: 'a\rb', escaped: 'a\\d b' },
    { value: 'a\fb', escaped: 'a\\c b' },
  ])('writes the line break in $value as a CSS hex escape, because a quoted CSS string cannot hold one', ({ value, escaped }) => {
    expect(cssAttributeValue(value)).toBe(escaped)
  })

  it.each([
    'live-child-read',
    'Run"Shell\\Command__1',
    'call|fc_1/x:y',
    'line\nbreak',
    'carriage\rreturn',
    'form\ffeed',
    '\\"\\',
    ']',
    '',
  ])('builds an attribute selector that a CSS parser matches to the exact value: %j', (value) => {
    const row = document.createElement('div')
    row.setAttribute('data-tool-call-id', value)
    const other = document.createElement('div')
    other.setAttribute('data-tool-call-id', `${value}-other`)
    document.body.replaceChildren(row, other)
    const selector = `[data-tool-call-id="${cssAttributeValue(value)}"]`
    expect(document.querySelectorAll(selector)).toHaveLength(1)
    expect(document.querySelector(selector)).toBe(row)
  })
})
