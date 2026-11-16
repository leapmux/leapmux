import { describe, expect, it } from 'vitest'
import { stubDocumentFonts } from '~/test-support/documentFonts'
import { fontLoadRetryDelayMs, fontVariantAvailable, fontVariantSpecs } from './fontFaces'

describe('fontLoadRetryDelayMs', () => {
  it.each([
    [0, 300],
    [1, 600],
    [2, 1200],
    [3, 2400],
  ])('waits the base, doubled for each earlier retry (%i earlier gives %i ms)', (retriesUsed, delayMs) => {
    expect(fontLoadRetryDelayMs(retriesUsed)).toBe(delayMs)
  })
})

describe('fontVariantSpecs', () => {
  it('gives one shorthand for each of the four faces, with the size and the family', () => {
    expect(fontVariantSpecs('"Hack NF", monospace', '1em')).toEqual([
      'normal normal 1em "Hack NF", monospace',
      'normal bold 1em "Hack NF", monospace',
      'italic normal 1em "Hack NF", monospace',
      'italic bold 1em "Hack NF", monospace',
    ])
  })
})

describe('fontVariantAvailable', () => {
  // jsdom implements no `document.fonts`, so each case stubs the API that it needs.
  it('answers what the font API answers', () => {
    stubDocumentFonts({
      check: spec => spec === 'normal normal 1em Loaded',
    })
    expect(fontVariantAvailable('normal normal 1em Loaded')).toBe(true)
    expect(fontVariantAvailable('normal normal 1em Missing')).toBe(false)
  })

  it('answers false when the check throws', () => {
    stubDocumentFonts({
      check: () => {
        throw new SyntaxError('unparseable font shorthand')
      },
    })
    expect(fontVariantAvailable('not a valid family')).toBe(false)
  })

  it('answers false when the document has no font API', () => {
    stubDocumentFonts(undefined)
    expect(fontVariantAvailable('normal normal 1em Mono')).toBe(false)
  })
})
