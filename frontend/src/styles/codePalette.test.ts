import { describe, expect, it } from 'vitest'
import { blendedCodeTint, blendedTint, CODE_BLOCK_TINT_PERCENT, CODE_BORDER_TINT_PERCENT, CODE_CARD_TINT_PERCENT, opaqueCodeTint } from '~/styles/codePalette'
import { ALL_VARIANTS, paletteColorToHex } from '~/styles/themes'
import { contrast, mixOver } from '~/test-support/color'

// The fields a code surface paints are DERIVED, so nothing in the catalogue
// states them and no reviewer sees them. This suite is what measures them, on
// every catalogued variant and on every surface the app can put a code block on:
// a step that is too small on one palette is a code block that vanishes on that
// theme alone, and it vanishes only in one of the three places.
//
// `mixOver` reproduces what the browser composites. A translucent tint over an
// opaque backdrop and `color-mix(in srgb, ...)` of two opaque colours are the
// same per-channel interpolation, so one helper serves both forms.

/**
 * The three surfaces the app paints UNDER a message body.
 *
 * A code block lands on all three -- the panel behind the composer, the `--card`
 * an assistant band paints, and the `--accent` of a user message's bubble -- and
 * the blended form is the answer to the fact that one opaque colour cannot
 * relate to all three.
 */
const HOSTS = ['--background', '--card', '--accent'] as const

/** A blended step of `percent`, composited over `backdrop`. */
function blended(palette: Record<string, string>, percent: number, backdrop: string): string {
  return mixOver(paletteColorToHex(palette['--foreground']!), String(percent), backdrop)
}

/** What a code block's field resolves to when it is hosted on `host`. */
function blendedField(palette: Record<string, string>, host: string): string {
  return blended(palette, CODE_BLOCK_TINT_PERCENT, paletteColorToHex(palette[host]!))
}

/** The field a block paints when the syntax theme's polarity opposes the app's. */
function opaqueField(palette: Record<string, string>): string {
  return blended(palette, CODE_BLOCK_TINT_PERCENT, paletteColorToHex(palette['--background']!))
}

describe('the code tint helpers', () => {
  it('spell the same step three ways, over three different bases', () => {
    // The variable names are a contract with the rules in ~/styles/global.css.ts
    // and with `codeSurfaceTheme`. Rename one there and not here and the
    // declaration is invalid -- which is not an error, so the surface silently
    // keeps whatever it inherited.
    expect(blendedTint(CODE_BLOCK_TINT_PERCENT)).toBe('rgb(from var(--foreground) r g b / 6%)')
    expect(blendedCodeTint(CODE_CARD_TINT_PERCENT)).toBe('rgb(from var(--code-foreground) r g b / 15%)')
    expect(opaqueCodeTint(CODE_BLOCK_TINT_PERCENT))
      .toBe('color-mix(in srgb, var(--code-foreground) 6%, var(--code-background))')
  })

  it('writes a percentage no float error can mangle', () => {
    // `0.075 * 100` is 7.500000000000001 in IEEE 754, and CSS would take it --
    // but the value is also what this suite measures and what a reader compares
    // against the stylesheet. The strengths are stated in percent for that
    // reason, and this pins it.
    for (const percent of [CODE_BLOCK_TINT_PERCENT, CODE_CARD_TINT_PERCENT, CODE_BORDER_TINT_PERCENT])
      expect(blendedTint(percent)).toMatch(/ \d+(?:\.\d)?%\)$/)
  })

  it('keeps inline code and a fenced block at one strength', () => {
    // The global `code, pre, kbd, samp, tt` rule and a block's field are built
    // from the same constant, so "inline code and a code block are the same idea
    // at the same weight" is a property rather than a coincidence two files
    // happen to agree on today.
    expect(blendedTint(CODE_BLOCK_TINT_PERCENT).replace('--foreground', '--code-foreground'))
      .toBe(blendedCodeTint(CODE_BLOCK_TINT_PERCENT))
  })
})

describe('a blended code block, on every host and every catalogued variant', () => {
  // The floors sit under the measured minimum for each pair, with room for a
  // palette added later, and far above 1.0 -- which is what a step that stopped
  // stepping would return.
  it('reads as a block on all three surfaces the app puts it on', () => {
    // The failure this answers: an OPAQUE field derived from the code page. It
    // measured as little as 1.051:1 against a user bubble's accent while sitting
    // up to 68.9 sRGB units away from it -- too flat to read as a step, too far
    // to belong to the bubble.
    for (const v of ALL_VARIANTS) {
      for (const host of HOSTS) {
        const backdrop = paletteColorToHex(v.palette[host]!)
        expect(contrast(blendedField(v.palette, host), backdrop), `${v.id}: block field on ${host}`)
          .toBeGreaterThan(1.04)
      }
    }
  })

  it('keeps the chrome ON that block outlined against it', () => {
    // The block itself is unbordered -- its field is the only thing marking it.
    // The copy button on it is not, and that outline is stepped from the FIELD:
    // derived from the code page it measured 1.0005:1 on ayu-light inside an
    // accent bubble. Its step is the weight each palette gives its OWN `--border`
    // (15.5%-23.5%, mean 18.9%), so chrome inside a code block is not outlined
    // more heavily than the rest of the UI.
    for (const v of ALL_VARIANTS) {
      for (const host of HOSTS) {
        const field = blendedField(v.palette, host)
        const border = blended(v.palette, CODE_BORDER_TINT_PERCENT, field)
        expect(contrast(border, field), `${v.id}: chrome border vs the field behind it, on ${host}`)
          .toBeGreaterThan(1.15)
      }
    }
  })

  it('keeps a chip visible on that block', () => {
    // `--code-card` is the copy button and the language label's hover, and it
    // blends for the same reason the field does: held opaque against a blended
    // field it measured 1.000:1 on one-light over an assistant band.
    for (const v of ALL_VARIANTS) {
      for (const host of HOSTS) {
        const field = blendedField(v.palette, host)
        expect(contrast(blended(v.palette, CODE_CARD_TINT_PERCENT, field), field), `${v.id}: chip on the field, on ${host}`)
          .toBeGreaterThan(1.15)
      }
    }
  })

  it('keeps code legible, within what the host itself allows', () => {
    // Recessing a field spends contrast, and on several variants the accent
    // bubble's OWN prose already measures 4.51-5.15:1, so a block inside one has
    // little to spend. This is what CAPS the tint: at the 6% it settled on the
    // worst case is 4.05:1, and 7% would take it to 3.98:1. The floor here is
    // what the palettes actually allow, and it fails both if a theme arrives with
    // an accent tighter than the tightest one shipped and if the tint is raised
    // past what the shipped ones can carry.
    for (const v of ALL_VARIANTS) {
      const text = paletteColorToHex(v.palette['--foreground']!)
      for (const host of HOSTS) {
        expect(contrast(text, blendedField(v.palette, host)), `${v.id}: code text on the field, on ${host}`)
          .toBeGreaterThan(4.0)
      }
    }
  })
})

describe('an opaque code block, for a syntax theme of the opposing polarity', () => {
  it('steps off the code page, which is what the baked tokens were made for', () => {
    // A tint cannot answer this case: over a light page it stays a light field,
    // and a dark theme's tokens on one measured a median 1.97:1.
    for (const v of ALL_VARIANTS) {
      const page = paletteColorToHex(v.palette['--background']!)
      expect(contrast(opaqueField(v.palette), page), `${v.id}: opaque field vs the code page`)
        .toBeGreaterThan(1.04)
    }
  })

  it('carries the same chip and chrome outline, stepped off that field', () => {
    for (const v of ALL_VARIANTS) {
      const field = opaqueField(v.palette)
      expect(contrast(blended(v.palette, CODE_CARD_TINT_PERCENT, field), field), `${v.id}: chip on the opaque field`)
        .toBeGreaterThan(1.15)
      expect(contrast(blended(v.palette, CODE_BORDER_TINT_PERCENT, field), field), `${v.id}: chrome border on the opaque field`)
        .toBeGreaterThan(1.15)
    }
  })

  it('keeps code legible on it', () => {
    for (const v of ALL_VARIANTS) {
      expect(contrast(paletteColorToHex(v.palette['--foreground']!), opaqueField(v.palette)), `${v.id}: code text on the opaque field`)
        .toBeGreaterThan(4.5)
    }
  })
})

describe('the sweep itself', () => {
  it('measures every variant the catalogue carries, on every host', () => {
    // Without this the sweeps above pass vacuously the day ALL_VARIANTS is empty
    // or HOSTS is narrowed -- and a green suite that measured nothing is the
    // failure the floors exist to prevent.
    expect(ALL_VARIANTS.length).toBeGreaterThanOrEqual(30)
    expect(HOSTS.length).toBe(3)
    for (const v of ALL_VARIANTS) {
      for (const host of HOSTS)
        expect(v.palette[host], `${v.id} states ${host}`).toBeTruthy()
    }
  })
})
