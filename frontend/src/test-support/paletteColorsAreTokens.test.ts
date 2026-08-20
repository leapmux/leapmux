import { readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { collectStyleFiles } from '~/test-support/styleFiles'

// A colour that plays a palette ROLE must read the palette, never a literal.
//
// There are thirty variants now, and a literal answers for exactly one of them.
// `PillGroup`'s selected pill paired `backgroundColor: 'var(--primary)'` with
// `color: '#ffffff'`, which was right for the palette it was written against and
// wrong for the twenty-one variants that publish `--primary-foreground: #000000`
// -- down to 1.49:1 on Ayu Mirage, inside the theme picker itself. The catalogue
// suite could not catch it: it floors `--primary-foreground` against `--primary`
// at 3:1, and the CSS ignored the token it was flooring.
//
// So the rule is checked where the mistake is written. A role colour has a token;
// if a new role needs one the catalogue grows it, which is what makes every
// variant answer for it.

const srcRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')

/** `color`, `backgroundColor` or `borderColor` set to a bare hex literal. */
const ROLE_COLOR_LITERAL = /(?:^|[^\w-])(color|backgroundColor|borderColor)\s*:\s*'#[0-9a-f]{3,8}'/gi

/**
 * Files whose literals are NOT palette roles.
 *
 * `ThinkingTokenCount` animates a spectrum: each stop is a named hue in a loop
 * (red -> orange -> yellow -> ...), not a role the palette has an opinion about,
 * and routing it through tokens would ask every theme to publish six hues it
 * never uses elsewhere.
 */
const NOT_PALETTE_ROLES = new Set([
  join(srcRoot, 'components', 'chat', 'widgets', 'ThinkingTokenCount.css.ts'),
])

describe('palette role colours come from the palette', () => {
  it('never sets a role colour to a hex literal', () => {
    const offenders: string[] = []
    for (const file of collectStyleFiles(srcRoot)) {
      if (NOT_PALETTE_ROLES.has(file))
        continue
      const source = readFileSync(file, 'utf8')
      for (const match of source.matchAll(ROLE_COLOR_LITERAL))
        offenders.push(`${relative(srcRoot, file)}: ${match[0].trim()}`)
    }
    expect(offenders, `these hardcode a colour the palette owns; read the token instead:\n  ${offenders.join('\n  ')}`)
      .toEqual([])
  })

  it('finds the files it is meant to be guarding', () => {
    // Without this the case above passes vacuously the day the glob or the
    // pattern stops matching, which is exactly when it needs to fail.
    expect(collectStyleFiles(srcRoot).length, 'no .css.ts found -- has the layout moved?')
      .toBeGreaterThanOrEqual(20)
  })
})
