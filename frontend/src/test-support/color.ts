// Colour maths for the palette suites: parse a token's value, then measure it.
//
// Several suites need the same functions -- `themes.test.ts` measures the
// catalogue's own tokens, `codePalette.test.ts` measures the fields a code
// surface DERIVES from them, and `ThemeSwatch.test.tsx` measures the nine it
// puts in a chip. A second copy of `luminance` is a second place for the WCAG
// coefficients to be wrong, and a wrong contrast floor passes silently.
//
// Two distance functions live here on purpose, and they answer different
// questions. `colorDistance` orders a ramp; `deltaE` says whether an eye can
// tell two fills apart. Each one's doc comment states when to reach for it.

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
 * Straight-line distance in sRGB, a rough stand-in for how different two fills
 * look.
 *
 * Cheap, and good enough to order one ramp against itself, which is what the
 * diff-tint cases use it for. It is NOT a measure of whether two colours look
 * alike, because sRGB is not perceptually even. Two pairs the catalogue
 * actually holds sit at the same distance by this function and nowhere near it
 * by eye: Ayu Light's `--card` and `--muted` measure 16.2 here and 3.2 delta-E
 * (indistinguishable), while GitHub Dark's `--lm-success-subtle` and `--faint`
 * measure 16.8 here and 19.0 delta-E (plainly different -- a dark green beside
 * a dark blue-grey). Use `deltaE` whenever the question is whether a reader can
 * tell two fills apart.
 */
export function colorDistance(a: string, b: string): number {
  const [x, y] = [parseColor(a), parseColor(b)]
  if (!x || !y)
    throw new Error(`cannot measure the distance between ${a} and ${b}`)
  return Math.hypot(x[0] - y[0], x[1] - y[1], x[2] - y[2])
}

/** One colour in CIE Lab, D65. */
function toLab([r, g, b]: Rgb): [number, number, number] {
  const linear = (v: number) => {
    const s = v / 255
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  const [lr, lg, lb] = [linear(r), linear(g), linear(b)]
  // sRGB to XYZ, then normalised by the D65 white point.
  const x = (lr * 0.4124 + lg * 0.3576 + lb * 0.1805) / 0.95047
  const y = lr * 0.2126 + lg * 0.7152 + lb * 0.0722
  const z = (lr * 0.0193 + lg * 0.1192 + lb * 0.9505) / 1.08883
  const f = (c: number) => c > 0.008856 ? Math.cbrt(c) : 7.787 * c + 16 / 116
  const [fx, fy, fz] = [f(x), f(y), f(z)]
  return [116 * fy - 16, 500 * (fx - fy), 200 * (fy - fz)]
}

/**
 * CIE76 delta-E: how different two colours LOOK, on a perceptually even scale.
 *
 * Approximately 1 is the smallest difference an eye can find, 2.3 is the
 * "just noticeable difference", and anything under about 5 reads as the same
 * colour at the size of an icon. Two neutrals differ by exactly the gap in
 * their L*, so black to white is 100.
 *
 * Prefer this to `colorDistance` whenever the question is whether a reader can
 * tell two fills apart. `ThemeSwatch` is the caller that needs it: the surface
 * ramp tokens it had to reject measure a respectable distance in sRGB and
 * almost nothing here.
 */
export function deltaE(a: string, b: string): number {
  const [x, y] = [parseColor(a), parseColor(b)]
  if (!x || !y)
    throw new Error(`cannot measure the difference between ${a} and ${b}`)
  const [la, aa, ba] = toLab(x)
  const [lb, ab, bb] = toLab(y)
  return Math.hypot(la - lb, aa - ab, ba - bb)
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
