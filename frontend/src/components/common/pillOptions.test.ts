import type { PillOptionSpec } from './pillOptions'
import { describe, expect, it } from 'vitest'
import { disambiguateLabels, isPillOptions } from './pillOptions'

function options(count: number): PillOptionSpec<string>[] {
  return Array.from({ length: count }, (_, index) => ({ key: `option-${index}`, label: `Option ${index}` }))
}

describe('isPillOptions', () => {
  it.each([1, 2, 3, 4])('accepts %s options within the control limit', (count) => {
    expect(isPillOptions(options(count))).toBe(true)
  })

  it.each([0, 5, 10_000])('refuses %s options outside the control limit', (count) => {
    expect(isPillOptions(options(count))).toBe(false)
  })

  it('reads a frozen option list without changing its entries or order', () => {
    const first = Object.freeze({ key: 'first', label: 'First' })
    const second = Object.freeze({ key: 'second', label: 'Second' })
    const input = Object.freeze([first, second])
    expect(isPillOptions(input)).toBe(true)
    expect(input).toEqual([first, second])
    expect(input[0]).toBe(first)
    expect(input[1]).toBe(second)
  })
})

/**
 * PillGroup refuses duplicate selection keys.
 * Distinct keys can still share a label, which confuses users and screen readers.
 * The pure helper uses each item's detail to distinguish every repeated label.
 */
describe('disambiguateLabels', () => {
  const label = (item: { label: string }) => item.label
  const distinct = (item: { label: string, detail: string }) => item.detail

  it('keeps a label that no other item shares', () => {
    expect(disambiguateLabels(
      [{ label: 'Once', detail: 'a' }, { label: 'Session', detail: 'b' }],
      label,
      distinct,
    )).toEqual(['Once', 'Session'])
  })

  it('replaces every member of a collision, not the later ones alone', () => {
    expect(disambiguateLabels(
      [{ label: 'Host rule', detail: 'Host: a' }, { label: 'Host rule', detail: 'Host: b' }],
      label,
      distinct,
    )).toEqual(['Host: a', 'Host: b'])
  })

  it('leaves an uncolliding neighbour alone while it replaces a collision', () => {
    expect(disambiguateLabels(
      [
        { label: 'Once', detail: 'Once' },
        { label: 'Host rule', detail: 'Host: a' },
        { label: 'Host rule', detail: 'Host: b' },
      ],
      label,
      distinct,
    )).toEqual(['Once', 'Host: a', 'Host: b'])
  })

  it('replaces all three of a three-way collision', () => {
    expect(disambiguateLabels(
      [
        { label: 'Host rule', detail: 'Host: a' },
        { label: 'Host rule', detail: 'Host: b' },
        { label: 'Host rule', detail: 'Host: c' },
      ],
      label,
      distinct,
    )).toEqual(['Host: a', 'Host: b', 'Host: c'])
  })

  it('gives an empty list back unchanged', () => {
    expect(disambiguateLabels([], label, distinct)).toEqual([])
  })
})
