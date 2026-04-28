import { describe, expect, it } from 'vitest'
import { acpSearchFromToolCall } from './search'

describe('acpSearchFromToolCall', () => {
  it('returns null for null/undefined', () => {
    expect(acpSearchFromToolCall(null)).toBeNull()
    expect(acpSearchFromToolCall(undefined)).toBeNull()
  })

  it('returns null when no metadata.matches and no text', () => {
    expect(acpSearchFromToolCall({})).toBeNull()
  })

  it('extracts matches from rawOutput.metadata', () => {
    const source = acpSearchFromToolCall({
      rawOutput: { metadata: { matches: 7 } },
    })
    expect(source).toEqual({
      variant: 'search',
      filenames: [],
      content: '',
      numFiles: 0,
      numLines: 0,
      matches: 7,
      truncated: false,
      fallbackContent: '',
    })
  })

  it('captures text fallback from content array', () => {
    const source = acpSearchFromToolCall({
      content: [{ type: 'content', content: { text: 'opaque text' } }],
    })
    expect(source?.fallbackContent).toBe('opaque text')
    expect(source?.matches).toBeUndefined()
  })

  it('captures rawOutput.output as fallback when no metadata.matches', () => {
    const source = acpSearchFromToolCall({
      rawOutput: { output: 'raw' },
    })
    expect(source?.fallbackContent).toBe('raw')
  })
})
