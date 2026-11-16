import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { fontVariantAvailable, fontVariantSpecs } from './fontFaces'

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
  // jsdom implements no `document.fonts`, so each case installs the API that it needs.
  let originalFonts: PropertyDescriptor | undefined

  function installFonts(check: (spec: string) => boolean): void {
    Object.defineProperty(document, 'fonts', {
      configurable: true,
      value: { check },
    })
  }

  beforeEach(() => {
    originalFonts = Object.getOwnPropertyDescriptor(document, 'fonts')
  })

  afterEach(() => {
    if (originalFonts)
      Object.defineProperty(document, 'fonts', originalFonts)
    else
      delete (document as { fonts?: unknown }).fonts
  })

  it('answers what the font API answers', () => {
    installFonts(spec => spec === 'normal normal 1em Loaded')
    expect(fontVariantAvailable('normal normal 1em Loaded')).toBe(true)
    expect(fontVariantAvailable('normal normal 1em Missing')).toBe(false)
  })

  it('answers false when the check throws', () => {
    installFonts(() => {
      throw new SyntaxError('unparseable font shorthand')
    })
    expect(fontVariantAvailable('not a valid family')).toBe(false)
  })

  it('answers false when the document has no font API', () => {
    delete (document as { fonts?: unknown }).fonts
    expect(fontVariantAvailable('normal normal 1em Mono')).toBe(false)
  })
})
