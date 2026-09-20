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
      filenames: [],
      content: '',
      numFiles: 0,
      numLines: 0,
      matchCount: 7,
      truncated: false,
      fallbackContent: '',
      empty: true,
    })
  })

  it('captures text fallback from content array', () => {
    const source = acpSearchFromToolCall({
      content: [{ type: 'content', content: { text: 'opaque text' } }],
    })
    expect(source?.fallbackContent).toBe('opaque text')
    expect(source?.matchCount).toBeUndefined()
  })

  it('captures rawOutput.output as fallback when no metadata.matches', () => {
    const source = acpSearchFromToolCall({
      rawOutput: { output: 'raw' },
    })
    expect(source?.fallbackContent).toBe('raw')
  })
})

/**
 * Whether the extractor RECOGNIZED an empty result.
 *
 * The protocol states a `matches` TOTAL rather than a sentence, and the shared tables
 * read no empty wording for any provider of this family. An empty body is therefore
 * the one empty result this build can recognize -- the same positive evidence the
 * renderer required before the flag existed. A stated zero reaches the row through
 * `matchCount`, which the search summary words on its own.
 */
describe('acpSearchFromToolCall empty results', () => {
  it('recognizes an empty result from an empty body', () => {
    expect(acpSearchFromToolCall({ rawOutput: { metadata: { matches: 0 } } })?.empty).toBe(true)
  })

  it('states no empty result for a body it did not classify', () => {
    const source = acpSearchFromToolCall({ content: [{ type: 'content', content: { text: 'nothing matched' } }] })
    expect(source?.empty).toBe(false)
    expect(source?.fallbackContent).toBe('nothing matched')
  })
})
