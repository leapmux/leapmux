import { describe, expect, it } from 'vitest'
import { joinClassNames } from './classNames'

describe('joinClassNames', () => {
  it('joins present classes and omits absent values', () => {
    expect(joinClassNames('one', false, undefined, 'two', null)).toBe('one two')
  })

  it('returns undefined when no class remains', () => {
    expect(joinClassNames(false, null, undefined, '')).toBeUndefined()
  })
})
