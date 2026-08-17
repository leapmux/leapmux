import { describe, expect, it } from 'vitest'
import { buildFontFamily, DEFAULT_MONO_FONT_FAMILY } from './fontStack'

describe('buildFontFamily', () => {
  it('quotes each family and joins with commas', () => {
    expect(buildFontFamily(['Inter', 'Noto Sans KR'])).toBe('"Inter", "Noto Sans KR"')
  })

  // The escape is the point of the helper. This value reaches a stylesheet
  // that xterm builds by CONCATENATION, so an unescaped quote would end the
  // declaration and the rest of the name would be read as live CSS. The
  // account write path refuses such a name, but the emitter must be safe
  // whatever a stored value holds — the browser tier reads from a document
  // a person can edit.
  it('escapes a quote so a name cannot end the declaration', () => {
    expect(buildFontFamily(['Ev"il'])).toBe('"Ev\\"il"')
  })

  it('escapes a backslash', () => {
    expect(buildFontFamily(['back\\slash'])).toBe('"back\\\\slash"')
  })

  // A control character does not end the declaration -- it makes the whole
  // declaration INVALID, and a CSS parser drops an invalid declaration
  // entirely. xterm then falls back to the PLATFORM font, and
  // `document.fonts.load` rejects the same spec with a SyntaxError.
  it('escapes a newline, a carriage return and a tab as CSS hex escapes', () => {
    expect(buildFontFamily(['My\nFont'])).toBe('"My\\a Font"')
    expect(buildFontFamily(['My\rFont'])).toBe('"My\\d Font"')
    expect(buildFontFamily(['My\tFont'])).toBe('"My\\9 Font"')
  })

  it('escapes NUL and DEL', () => {
    expect(buildFontFamily(['a\u0000b'])).toBe('"a\\0 b"')
    expect(buildFontFamily(['a\u007Fb'])).toBe('"a\\7f b"')
  })

  // The trailing space is what ENDS the hex digits. Without it `\a` before
  // a literal `b` reads as the single code point U+00AB, so the escape
  // silently renames the family.
  it('always emits the space that ends a hex escape', () => {
    expect(buildFontFamily(['\naa'])).toBe('"\\a aa"')
  })

  // ORDER: the control-character pass emits backslashes, so running it
  // first would leave the quote pass escaping those and turn each escape
  // into literal text.
  it('escapes a backslash and a control character in the same name exactly once', () => {
    expect(buildFontFamily(['a\\b\nc'])).toBe('"a\\\\b\\a c"')
  })

  // The declaration the escape protects must actually PARSE. jsdom keeps
  // the CSSOM's own serializer, so an invalid font-family round-trips as
  // the empty string exactly as a browser drops it.
  it('yields a font-family declaration the CSSOM accepts', () => {
    const el = document.createElement('div')
    el.style.fontFamily = `${buildFontFamily(['My\nFont', 'Ev"il', 'back\\slash'])}, monospace`
    expect(el.style.fontFamily).not.toBe('')
    expect(el.style.fontFamily).toContain('monospace')
  })

  it('yields an empty string for an empty stack', () => {
    expect(buildFontFamily([])).toBe('')
  })
})

describe('default mono font stack (DEFAULT_MONO_FONT_FAMILY)', () => {
  // Three consumers fall back to it — the terminal instance, the resolved
  // preference, and the `--font-mono` custom property — and they must
  // agree. It lived as three separate literals, so changing the bundled
  // font left two of them stale with no signal.
  it('names the bundled face first', () => {
    expect(DEFAULT_MONO_FONT_FAMILY.startsWith('"Hack NF"')).toBe(true)
  })

  it('ends at a generic family, so it always resolves', () => {
    expect(DEFAULT_MONO_FONT_FAMILY.endsWith('monospace')).toBe(true)
  })
})
