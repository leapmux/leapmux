import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { paletteColorToHex, resolveVariant } from '~/styles/themes'
import { defaultTheme } from '~/styles/themes/default'

/**
 * `public/manifest.webmanifest` is a static JSON document: it cannot import the
 * palette, and JSON carries no comment to say what its colours must equal. So
 * this case is the link.
 *
 * It exists because those literals HAD drifted. The manifest said `#F7F5F2`,
 * `entry-server.tsx` said the same, app.tsx wrote `#ffffff` at runtime, and the
 * palette's actual light `--background` was `rgb(255 254 252)` -- four values
 * for one colour, on the surfaces a user sees before the app has painted
 * anything. `entry-server.tsx` now derives its tag from the palette and needs
 * no case here; the manifest cannot, so it gets one.
 *
 * A manifest colour is stated in HEX rather than the palette's `rgb()`: the
 * space-separated CSS Color 4 form is not something every manifest parser
 * accepts, and a colour a parser rejects falls back to the browser default.
 */
// Through the PRODUCTION converter, not a second regex of this file's own.
//
// Not tautological: the subject here is the manifest JSON literal, and
// `paletteColorToHex` carries its own independent coverage in
// ~/styles/themes/themes.test.ts -- both `rgb()` spellings, a hex passthrough,
// an `oklch()` passthrough, and a sweep over every palette. A private copy
// asserted the same conversion a second time and could drift from the one the
// app actually ships.
function toHex(rgb: string): string {
  const hex = paletteColorToHex(rgb)
  if (!hex.startsWith('#'))
    throw new Error(`the default light --background is not a plain rgb() triple: ${rgb}`)
  return hex
}

describe('web app manifest colours', () => {
  it('states the default theme\'s light background', () => {
    const manifest = JSON.parse(
      readFileSync(join(import.meta.dirname, '../../../public/manifest.webmanifest'), 'utf-8'),
    ) as { theme_color: string, background_color: string }

    const expected = toHex(resolveVariant(defaultTheme, undefined, 'light').palette['--background']!)
    expect(manifest.theme_color.toLowerCase()).toBe(expected)
    expect(manifest.background_color.toLowerCase()).toBe(expected)
  })
})
