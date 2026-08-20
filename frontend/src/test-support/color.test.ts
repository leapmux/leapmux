import { describe, expect, it } from 'vitest'
import { colorAlpha, colorDistance, contrast, deltaE, luminance, mixOver, parseColor } from '~/test-support/color'

/**
 * The palette suites measure THROUGH these functions: `themes.test.ts` checks the
 * catalogue's own tokens, `codePalette.test.ts` checks the fields a code surface
 * derives from them, and `ThemeSwatch.test.tsx` checks that the nine colours in a
 * theme chip stay apart. All three assert floors that only mean something if the
 * instrument is right. A wrong WCAG coefficient, a wrong Lab white point, or a
 * regex that silently fails to parse would not fail any of them -- it would move
 * every floor at once and keep passing. So the instrument is pinned here against
 * values the standards fix, not against values these functions produce.
 */
describe('parseColor', () => {
  it('reads a six-digit hex, in either case', () => {
    expect(parseColor('#000000')).toEqual([0, 0, 0])
    expect(parseColor('#ffffff')).toEqual([255, 255, 255])
    expect(parseColor('#FF8000')).toEqual([255, 128, 0])
    expect(parseColor('#0a0B0c')).toEqual([10, 11, 12])
  })

  it('reads both rgb() separators, with or without an alpha', () => {
    // getComputedStyle answers with the comma form on some engines and the
    // space-separated form on others, so both have to land on the same channels.
    expect(parseColor('rgb(1 2 3)')).toEqual([1, 2, 3])
    expect(parseColor('rgb(1, 2, 3)')).toEqual([1, 2, 3])
    expect(parseColor('rgba(1, 2, 3, 0.5)')).toEqual([1, 2, 3])
    expect(parseColor('rgba(1 2 3 / 50%)')).toEqual([1, 2, 3])
  })

  it('trims the surrounding space a token value carries', () => {
    expect(parseColor('  #ffffff  ')).toEqual([255, 255, 255])
    expect(parseColor(' rgb(1, 2, 3) ')).toEqual([1, 2, 3])
  })

  it('returns undefined for a value it must not guess at', () => {
    // The caller reports WHICH token it could not read. Returning a zero here
    // would instead measure black and quietly pass a contrast floor.
    for (const value of [
      'var(--foreground)',
      'color-mix(in srgb, #fff 50%, #000)',
      'transparent',
      'currentColor',
      'red',
      '#fff',
      '#1234567',
      '',
      'rgb(1, 2)',
    ])
      expect(parseColor(value)).toBeUndefined()
  })
})

describe('luminance', () => {
  it('pins the two endpoints the standard fixes', () => {
    expect(luminance([255, 255, 255])).toBeCloseTo(1, 10)
    expect(luminance([0, 0, 0])).toBe(0)
  })

  it('weights the channels as WCAG does, green heaviest and blue lightest', () => {
    // The coefficients themselves: a full channel alone must equal its weight.
    expect(luminance([255, 0, 0])).toBeCloseTo(0.2126, 10)
    expect(luminance([0, 255, 0])).toBeCloseTo(0.7152, 10)
    expect(luminance([0, 0, 255])).toBeCloseTo(0.0722, 10)
  })

  it('takes the linear branch at or below the 0.03928 knee, and the curve above it', () => {
    // s = v/255 crosses 0.03928 between v=10 and v=11, so this pair straddles the
    // branch. Both are computed from the formula, not copied from the function.
    expect(luminance([10, 10, 10])).toBeCloseTo((10 / 255) / 12.92, 12)
    expect(luminance([11, 11, 11])).toBeCloseTo((((11 / 255) + 0.055) / 1.055) ** 2.4, 12)
  })

  it('rises with brightness', () => {
    const ramp = [0, 32, 64, 128, 192, 255].map(v => luminance([v, v, v]))
    for (let i = 1; i < ramp.length; i++)
      expect(ramp[i]!).toBeGreaterThan(ramp[i - 1]!)
  })
})

describe('contrast', () => {
  it('pins the two ratios the standard fixes', () => {
    expect(contrast('#000000', '#ffffff')).toBeCloseTo(21, 6)
    expect(contrast('#7f7f7f', '#7f7f7f')).toBeCloseTo(1, 10)
  })

  it('does not depend on which colour is named first', () => {
    // The implementation sorts the luminances, so a caller cannot get a ratio
    // below 1 by passing the darker colour as the background.
    expect(contrast('#ffffff', '#000000')).toBeCloseTo(contrast('#000000', '#ffffff'), 12)
    expect(contrast('#123456', '#abcdef')).toBeCloseTo(contrast('#abcdef', '#123456'), 12)
  })

  it('accepts the rgb() form the DOM answers with', () => {
    expect(contrast('rgb(0, 0, 0)', 'rgb(255, 255, 255)')).toBeCloseTo(21, 6)
  })

  it('throws on a colour it cannot read, rather than measuring a zero', () => {
    // Silence here is the failure mode that matters: an unreadable token that
    // measured as black would pass most floors and hide a broken palette.
    expect(() => contrast('var(--fg)', '#ffffff')).toThrow(/cannot measure contrast/)
    expect(() => contrast('#ffffff', 'color-mix(in srgb, #fff 50%, #000)')).toThrow(/cannot measure contrast/)
  })
})

describe('colorAlpha', () => {
  it('reads the slash form Chromium answers with for a relative colour', () => {
    // THE FORM THAT BROKE THREE SUITES. A blended code field is declared as
    // `rgb(from var(--foreground) r g b / 6%)`, and `getComputedStyle` reports it
    // as `color(srgb ...)` -- not as `rgba()`. A parser that matched `rgba()` and
    // fell back to 1 called every one of them opaque, and the assertions that
    // were meant to prove the field composites passed while measuring nothing.
    expect(colorAlpha('color(srgb 0.180392 0.203922 0.25098 / 0.06)')).toBeCloseTo(0.06, 5)
    expect(colorAlpha('rgb(34 32 30 / 0.06)')).toBeCloseTo(0.06, 5)
  })

  it('reads the legacy comma form', () => {
    expect(colorAlpha('rgba(34, 32, 30, 0.06)')).toBeCloseTo(0.06, 5)
    expect(colorAlpha('rgba(0, 0, 0, 0)')).toBe(0)
  })

  it('reads a percentage alpha, which CSS also permits', () => {
    expect(colorAlpha('color(srgb 0.1 0.2 0.3 / 6%)')).toBeCloseTo(0.06, 5)
  })

  it('answers 1 for an opaque colour in either notation', () => {
    expect(colorAlpha('rgb(255, 254, 252)')).toBe(1)
    expect(colorAlpha('color(srgb 0.869608 0.882255 0.903922)')).toBe(1)
  })

  it('throws on a form it does not know, rather than calling it opaque', () => {
    // Returning 1 is what let the serialization above go unnoticed. A colour this
    // cannot read has to stop the test, not quietly satisfy it.
    expect(() => colorAlpha('transparent')).toThrow(/unrecognised/)
    expect(() => colorAlpha('oklch(0.7 0.1 200 / 0.5)')).not.toThrow()
    expect(() => colorAlpha('#ffffff')).toThrow(/unrecognised/)
  })
})

describe('mixOver', () => {
  it('returns the background at 0% and the foreground at 100%', () => {
    expect(mixOver('#ff8000', '0%', '#123456')).toBe('#123456')
    expect(mixOver('#ff8000', '100%', '#123456')).toBe('#ff8000')
  })

  it('interpolates each channel at the halfway point', () => {
    expect(mixOver('#ffffff', '50%', '#000000')).toBe('#808080')
    expect(mixOver('#ff0000', '50%', '#0000ff')).toBe('#800080')
  })

  it('matches the tint percentage the code palette actually mixes', () => {
    // 7.5% of white over black is 0.075 * 255 = 19.125, which rounds to 19 = 0x13.
    expect(mixOver('#ffffff', '7.5%', '#000000')).toBe('#131313')
  })

  it('pads a channel that needs two hex digits', () => {
    // Without padStart a channel below 0x10 would shorten the string and produce
    // a colour no parser reads back.
    const mixed = mixOver('#ffffff', '2%', '#000000')
    expect(mixed).toBe('#050505')
    expect(parseColor(mixed)).toEqual([5, 5, 5])
  })

  it('reads a percentage with no trailing sign, as CSS also permits in a token', () => {
    expect(mixOver('#ffffff', '50', '#000000')).toBe('#808080')
  })

  it('throws when either colour is unreadable', () => {
    expect(() => mixOver('var(--fg)', '50%', '#000000')).toThrow(/cannot mix/)
    expect(() => mixOver('#ffffff', '50%', 'transparent')).toThrow(/cannot mix/)
  })
})

describe('colorDistance', () => {
  it('pins the diagonal of the sRGB cube', () => {
    // The largest distance the function can report: all three channels at full
    // travel. sqrt(3) * 255, from the definition and not from the function.
    expect(colorDistance('#000000', '#ffffff')).toBeCloseTo(Math.sqrt(3) * 255, 10)
  })

  it('reports zero for one colour against itself, in either spelling', () => {
    expect(colorDistance('#123456', '#123456')).toBe(0)
    expect(colorDistance('rgb(18 52 86)', '#123456')).toBe(0)
  })

  it('does not depend on which colour is named first', () => {
    expect(colorDistance('#123456', '#abcdef')).toBeCloseTo(colorDistance('#abcdef', '#123456'), 12)
  })

  it('measures one channel at a time', () => {
    expect(colorDistance('#000000', '#ff0000')).toBeCloseTo(255, 10)
    expect(colorDistance('#000000', '#0000ff')).toBeCloseTo(255, 10)
    // Two channels at full travel: sqrt(2) * 255.
    expect(colorDistance('#000000', '#ffff00')).toBeCloseTo(Math.SQRT2 * 255, 10)
  })

  it('throws on a colour it cannot read, rather than measuring a zero', () => {
    expect(() => colorDistance('var(--fg)', '#ffffff')).toThrow(/cannot measure the distance/)
    expect(() => colorDistance('#ffffff', 'transparent')).toThrow(/cannot measure the distance/)
  })
})

describe('deltaE', () => {
  it('pins the L* range the standard fixes', () => {
    // Black and white are both neutral, so their difference is the whole L*
    // axis: 0 to 100. This is the one value CIE Lab fixes exactly.
    expect(deltaE('#000000', '#ffffff')).toBeCloseTo(100, 5)
  })

  it('measures two neutrals as the gap in their lightness', () => {
    // sRGB #777777 is the classic L* = 50 anchor, so it must sit halfway
    // between black and white on this scale.
    expect(deltaE('#777777', '#000000')).toBeCloseTo(50, 1)
    expect(deltaE('#777777', '#ffffff')).toBeCloseTo(50, 1)
  })

  it('reports zero for one colour against itself, in either spelling', () => {
    expect(deltaE('#123456', '#123456')).toBe(0)
    expect(deltaE('rgb(18, 52, 86)', '#123456')).toBe(0)
  })

  it('does not depend on which colour is named first', () => {
    expect(deltaE('#123456', '#abcdef')).toBeCloseTo(deltaE('#abcdef', '#123456'), 12)
  })

  it('separates two hues further than the lightness axis alone can', () => {
    // Red and green share a lightness band, so a scale that only tracked
    // brightness would call them close. They are the furthest apart of any
    // pair in the sRGB gamut on this scale.
    expect(deltaE('#ff0000', '#00ff00')).toBeGreaterThan(100)
  })

  it('disagrees with colorDistance exactly where the eye does', () => {
    // THE REASON THIS FUNCTION EXISTS, in two pairs the catalogue really
    // holds. sRGB rates them as the same distance apart, within 4%. By eye one
    // is a single flat colour and the other is a green beside a blue-grey --
    // and ThemeSwatch had to reject the surface ramp on exactly this evidence.
    const rampPair = ['#f8f9fa', '#eff0f0'] as const // Ayu Light --card / --muted
    const huePair = ['#162a19', '#181c22'] as const // GitHub Dark --lm-success-subtle / --faint

    expect(colorDistance(...rampPair)).toBeCloseTo(16.2, 0)
    expect(colorDistance(...huePair)).toBeCloseTo(16.8, 0)

    expect(deltaE(...rampPair)).toBeLessThan(5)
    expect(deltaE(...huePair)).toBeGreaterThan(15)
    // The ordering inverts outright: sRGB calls the ramp pair the WIDER of the
    // two, and it is the one nobody can see.
    expect(deltaE(...rampPair)).toBeLessThan(deltaE(...huePair))
  })

  it('rises as a colour moves away, so a floor can be compared against it', () => {
    const steps = ['#000000', '#202020', '#606060', '#a0a0a0', '#ffffff']
      .map(v => deltaE('#000000', v))
    for (let i = 1; i < steps.length; i++)
      expect(steps[i]!).toBeGreaterThan(steps[i - 1]!)
  })

  it('throws on a colour it cannot read, rather than measuring a zero', () => {
    // Silence is the failure mode that matters here too: ThemeSwatch asserts a
    // separation floor on all thirty variants, and an unreadable token that
    // measured as black would clear that floor and prove nothing.
    expect(() => deltaE('var(--fg)', '#ffffff')).toThrow(/cannot measure the difference/)
    expect(() => deltaE('#ffffff', 'color-mix(in srgb, #fff 50%, #000)')).toThrow(/cannot measure the difference/)
  })
})
