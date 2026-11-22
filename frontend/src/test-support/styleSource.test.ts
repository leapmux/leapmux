import { describe, expect, it } from 'vitest'
import { extractBalancedBlock, extractStyleCallBlock } from './styleSource'

describe('extractBalancedBlock', () => {
  it('returns the interior of a balanced block with nested delimiters', () => {
    const src = 'prefix { a: { b: 1 }, c: 2 } suffix'
    const open = src.indexOf('{')
    expect(extractBalancedBlock(src, open, '{', '}')).toBe(' a: { b: 1 }, c: 2 ')
  })

  it('returns undefined when openIndex is out of range, mismatched, or unclosed', () => {
    expect(extractBalancedBlock('{ a: 1 }', -1, '{', '}')).toBeUndefined()
    expect(extractBalancedBlock('{ a: 1 }', 1, '{', '}')).toBeUndefined()
    expect(extractBalancedBlock('{ a: { b: 1 }', 0, '{', '}')).toBeUndefined()
  })
})

describe('extractStyleCallBlock', () => {
  it('extracts both object and array-composed style declarations with nested parens', () => {
    const src = [
      'export const box = style({ padding: \'var(--space-1)\', maxWidth: \'calc(100vw - 16px)\' })',
      'export const list = style([box, { overflowY: \'auto\' }])',
    ].join('\n')

    expect(extractStyleCallBlock(src, 'box')).toBe('{ padding: \'var(--space-1)\', maxWidth: \'calc(100vw - 16px)\' }')
    expect(extractStyleCallBlock(src, 'list')).toBe('[box, { overflowY: \'auto\' }]')
  })

  it('throws on missing or unclosed style exports', () => {
    expect(() => extractStyleCallBlock('export const other = style({})', 'missing')).toThrow('missing style export missing')
    expect(() => extractStyleCallBlock('export const broken = style({ padding: \'var(--space-1)\'', 'broken')).toThrow('unclosed style call for broken')
  })
})
