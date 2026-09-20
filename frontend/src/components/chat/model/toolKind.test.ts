import { describe, expect, it } from 'vitest'
import { TOOL_KINDS, toolKind } from './toolKind'

describe('toolKind', () => {
  it('keeps a kind the shared tables know', () => {
    expect(toolKind('move')).toBe('move')
  })

  // Cursor sends this one, and it is the only Agent Client Protocol kind the
  // shared table used to miss. The token spells the WIRE word, so the narrowing
  // keeps it rather than dropping the row into the uncategorized bucket.
  it('keeps the protocol mode switch under its own kind', () => {
    expect(toolKind('switch_mode')).toBe('switch_mode')
  })

  it('maps an unknown wire kind to other', () => {
    expect(toolKind('frobnicate')).toBe('other')
  })

  it('keeps the absent kind apart from other', () => {
    expect(toolKind('')).toBe('other')
    expect(toolKind(undefined)).toBe('unspecified')
  })

  it('narrows every kind it declares to itself', () => {
    for (const kind of TOOL_KINDS)
      expect(toolKind(kind)).toBe(kind)
  })
})
