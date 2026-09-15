import { describe, expect, it } from 'vitest'
import { TOOL_KINDS, toolKind, toolKindIcon, toolKindLabel } from './toolKind'

describe('toolKind', () => {
  it('keeps a kind the shared tables know', () => {
    expect(toolKind('move')).toBe('move')
  })

  it('maps an unknown wire kind to other', () => {
    expect(toolKind('switch_mode')).toBe('other')
    expect(toolKind('frobnicate')).toBe('other')
  })

  it('keeps the absent kind apart from other', () => {
    expect(toolKind('')).toBe('')
    expect(toolKind(undefined)).toBe('')
  })
})

describe('toolKindIcon', () => {
  // Reasonix reports `move` for a file rename. The icon table had no entry for
  // it, so the row drew the same generic wrench an unclassified tool draws.
  it('gives a move its own icon', () => {
    expect(toolKindIcon('move')).not.toBe(toolKindIcon('other'))
  })

  it('draws the generic icon for the unclassified kinds alone', () => {
    const generic = toolKindIcon('other')
    expect(TOOL_KINDS.filter(kind => toolKindIcon(kind) === generic)).toEqual(['', 'other', 'think'])
  })
})

describe('toolKindLabel', () => {
  it('labels a move', () => {
    expect(toolKindLabel('move')).toBe('Move')
  })

  it('labels a tool whose provider stated no kind', () => {
    expect(toolKindLabel('')).toBe('Tool')
  })

  it('gives every kind a capitalized label', () => {
    for (const kind of TOOL_KINDS)
      expect(toolKindLabel(kind)).toMatch(/^[A-Z]/)
  })
})
