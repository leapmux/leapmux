// Colour maths for the palette suites: parse a token's value, then measure it.
//
// Two suites need the same four functions -- `themes.test.ts` measures the
// catalogue's own tokens, and `codePalette.test.ts` measures the fields a code
// surface DERIVES from them. A second copy of `luminance` is a second place for
// the WCAG coefficients to be wrong, and a wrong contrast floor passes silently.

/** One colour as 8-bit sRGB channels. */
export type Rgb = [number, number, number]

/**
 * Read `#rrggbb`, `rgb(r g b)` or `rgba(r, g, b, a)` into channels.
 *
 * Returns undefined for anything else -- a `var()` indirection, a `color-mix()`
 * expression, `transparent` -- so a caller can report which token it could not
 * read instead of measuring a zero.
 */
export function parseColor(value: string): Rgb | undefined {
  const hex = /^#([0-9a-f]{6})$/i.exec(value.trim())
  if (hex) {
    const n = Number.parseInt(hex[1]!, 16)
    return [(n >> 16) & 0xFF, (n >> 8) & 0xFF, n & 0xFF]
  }
  const fn = /^rgba?\(\s*(\d+)[\s,]+(\d+)[\s,]+(\d+)/i.exec(value.trim())
  if (fn)
    return [Number(fn[1]), Number(fn[2]), Number(fn[3])]
  return undefined
}

/** WCAG 2.x relative luminance. */
export function luminance([r, g, b]: Rgb): number {
  const channel = (v: number) => {
    const s = v / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

/**
 * `color-mix(in srgb, fg <pct>, bg)`, as a hex string.
 *
 * The browser mixes in premultiplied sRGB, which for two OPAQUE colours is the
 * per-channel interpolation done here. Pass an opaque `bg`; `transparent` as the
 * second colour is a different calculation and this does not do it.
 */
export function mixOver(fg: string, pct: string, bg: string): string {
  const p = Number.parseFloat(pct) / 100
  const [f, b] = [parseColor(fg), parseColor(bg)]
  if (!f || !b)
    throw new Error(`cannot mix ${fg} over ${bg}`)
  return `#${f.map((c, i) => Math.round(c * p + b[i]! * (1 - p)).toString(16).padStart(2, '0')).join('')}`
}

/** WCAG 2.x contrast ratio, 1 (identical) to 21 (black on white). */
export function contrast(fg: string, bg: string): number {
  const a = parseColor(fg)
  const b = parseColor(bg)
  if (!a || !b)
    throw new Error(`cannot measure contrast between ${fg} and ${bg}`)
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

/**
 * The alpha of a colour `getComputedStyle` reported.
 *
 * THROWS on a form it does not know, and that is the point. Chromium serializes
 * a relative colour or a `color-mix()` as `color(srgb r g b / a)`, NOT as
 * `rgba()` -- so a parser that matched `rgba()` and fell back to 1 reported
 * every blended field as opaque and passed a suite that was measuring nothing.
 * A colour this cannot read is a test that has stopped testing.
 */
export function colorAlpha(color: string): number {
  const toNumber = (raw: string) => raw.endsWith('%') ? Number.parseFloat(raw) / 100 : Number(raw)
  // `color(srgb r g b / a)` and `rgb(r g b / a)` -- the modern slash form.
  const slash = /\/\s*([\d.]+%?)\s*\)$/.exec(color)
  if (slash)
    return toNumber(slash[1]!)
  // The legacy comma form, which carries alpha only as `rgba()`.
  const legacy = /^rgba\(([^)]*)\)$/.exec(color)
  if (legacy) {
    const parts = legacy[1]!.split(',')
    if (parts.length === 4)
      return toNumber(parts[3]!.trim())
  }
  if (/^(?:rgb|color)\(/.test(color))
    return 1
  throw new Error(`colorAlpha: unrecognised colour "${color}"`)
}
