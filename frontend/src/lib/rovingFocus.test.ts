import { describe, expect, it } from 'vitest'
import { nextRovingValue } from './rovingFocus'

describe('nextRovingValue', () => {
  const values = ['all', 'changed', 'staged', 'unstaged'] as const
  const event = (key: string, modifiers: Partial<KeyboardEvent> = {}) => ({
    altKey: false,
    ctrlKey: false,
    key,
    metaKey: false,
    ...modifiers,
  })

  it('moves forward and backward', () => {
    expect(nextRovingValue(values, 'changed', event('ArrowRight'))).toEqual({ value: 'staged' })
    expect(nextRovingValue(values, 'changed', event('ArrowLeft'))).toEqual({ value: 'all' })
    expect(nextRovingValue(values, 'changed', event('ArrowDown'))).toEqual({ value: 'staged' })
    expect(nextRovingValue(values, 'changed', event('ArrowUp'))).toEqual({ value: 'all' })
  })

  it('wraps at both ends', () => {
    expect(nextRovingValue(values, 'unstaged', event('ArrowRight'))).toEqual({ value: 'all' })
    expect(nextRovingValue(values, 'all', event('ArrowLeft'))).toEqual({ value: 'unstaged' })
  })

  it('moves to each end with Home and End', () => {
    expect(nextRovingValue(values, 'staged', event('Home'))).toEqual({ value: 'all' })
    expect(nextRovingValue(values, 'staged', event('End'))).toEqual({ value: 'unstaged' })
  })

  it('ignores keys that the control does not own', () => {
    expect(nextRovingValue(values, 'staged', event('a'))).toBeUndefined()
    expect(nextRovingValue(values, 'staged', event('Enter'))).toBeUndefined()
  })

  it('ignores browser and platform shortcuts', () => {
    expect(nextRovingValue(values, 'staged', event('ArrowLeft', { altKey: true }))).toBeUndefined()
    expect(nextRovingValue(values, 'staged', event('ArrowLeft', { ctrlKey: true }))).toBeUndefined()
    expect(nextRovingValue(values, 'staged', event('ArrowLeft', { metaKey: true }))).toBeUndefined()
  })

  it('owns no key when the set is empty', () => {
    expect(nextRovingValue([] as const, 'all', event('ArrowRight'))).toBeUndefined()
    expect(nextRovingValue([] as const, 'all', event('Home'))).toBeUndefined()
  })

  it('starts from the first value when the current value is absent', () => {
    expect(nextRovingValue(values, 'gone' as 'all', event('ArrowRight'))).toEqual({ value: 'changed' })
  })

  it('stays on the only value in a single-value set', () => {
    const one = ['all'] as const
    expect(nextRovingValue(one, 'all', event('ArrowRight'))).toEqual({ value: 'all' })
    expect(nextRovingValue(one, 'all', event('ArrowLeft'))).toEqual({ value: 'all' })
    expect(nextRovingValue(one, 'all', event('End'))).toEqual({ value: 'all' })
  })

  it('returns undefined and NaN values as tagged destinations', () => {
    expect(nextRovingValue(['set', undefined], 'set', event('ArrowRight')))
      .toEqual({ value: undefined })
    expect(nextRovingValue([0, Number.NaN], 0, event('ArrowRight')))
      .toEqual({ value: Number.NaN })
    expect(nextRovingValue([0, Number.NaN], Number.NaN, event('ArrowLeft')))
      .toEqual({ value: 0 })
  })
})
