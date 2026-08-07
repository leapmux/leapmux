import { describe, expect, it } from 'vitest'

import { darkPalette, lightPalette } from '~/styles/palette'

// This module has two consumers that cannot check each other: global.css.ts
// spreads it into the app's themes, and scripts/generate-notice.mjs inlines it
// into the standalone NOTICE.html page. A malformed entry surfaces as a colour
// that silently falls back to Oat's default on one surface or the other.
describe('palette', () => {
  it('declares every token as a non-empty CSS custom property', () => {
    for (const [name, palette] of [['light', lightPalette], ['dark', darkPalette]] as const) {
      for (const [token, value] of Object.entries(palette)) {
        expect(token, `${name}: ${token} is not a custom property`).toMatch(/^--[a-z0-9-]+$/)
        expect(value.trim(), `${name}: ${token} has no value`).not.toBe('')
      }
    }
  })

  it('defines no token that only the light theme has', () => {
    // A light-only token leaves the dark theme silently inheriting Oat's
    // value, which is how a palette gets a hole nobody notices until someone
    // switches themes.
    //
    // The reverse is allowed and used: --lm-opencode-inner/outer are dark-only
    // because AgentProviderIcon.tsx reads them as `var(--token, <light value>)`,
    // so the light value lives at the point of use rather than here.
    const lightOnly = Object.keys(lightPalette).filter(token => !(token in darkPalette))

    expect(lightOnly, `defined for light but not dark:\n  ${lightOnly.join('\n  ')}`).toHaveLength(0)
  })

  it('carries the tokens both consumers rely on', () => {
    // Not an exhaustive list -- just the ones whose absence would be visible
    // immediately on either surface.
    for (const token of ['--background', '--foreground', '--primary', '--accent', '--border']) {
      expect(lightPalette, `light theme is missing ${token}`).toHaveProperty([token])
      expect(darkPalette, `dark theme is missing ${token}`).toHaveProperty([token])
    }
  })
})
