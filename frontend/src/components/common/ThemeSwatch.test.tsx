import type { ThemeVariant } from '~/styles/themes'
import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { PIP_GRID_COLUMNS, PIP_GRID_PIPS } from '~/components/common/PipGrid'
import { ALL_VARIANTS, resolveVariant, themeById } from '~/styles/themes'
import { deltaE, parseColor } from '~/test-support/color'
import { SWATCH_TOKENS, ThemeSwatch } from './ThemeSwatch'

afterEach(cleanup)

/**
 * The floors the token choice was measured against, set below what the
 * catalogue actually reaches so a new theme has a little room without being
 * able to reintroduce a chip nobody can read.
 *
 * Measured today: 12.1 in-row/in-column, 9.4 against the background.
 */
const MIN_NEIGHBOUR_DELTA_E = 10
const MIN_BACKGROUND_DELTA_E = 8

function variantById(id: string): ThemeVariant {
  const found = ALL_VARIANTS.find(v => v.id === id)
  if (!found)
    throw new Error(`no variant ${id}`)
  return found
}

function pipFills(container: HTMLElement): (string | null)[] {
  return [...container.querySelectorAll('rect')].map(r => r.getAttribute('fill'))
}

describe('themeSwatch', () => {
  it('fills the nine pips from the variant palette, in token order', () => {
    const variant = variantById('default-light')
    const { container } = render(() => <ThemeSwatch variant={variant} />)

    expect(pipFills(container)).toEqual(SWATCH_TOKENS.map(t => variant.palette[t]))
    // Spot-check against the literal palette so the case cannot pass by
    // comparing the component's output with itself.
    expect(pipFills(container)[0]).toBe('rgb(13 148 136)')
  })

  it('paints the chip with the palette background', () => {
    const variant = variantById('default-light')
    const { container } = render(() => <ThemeSwatch variant={variant} />)
    const chip = container.firstElementChild as HTMLElement

    // Through `parseColor` because the DOM re-spells the value: the palette
    // states `rgb(255 254 252)` and `style.backgroundColor` reads back the
    // comma form. Other themes state hex, which it re-spells too.
    expect(parseColor(chip.style.backgroundColor)).toEqual([255, 254, 252])
    expect(parseColor(variant.palette['--background']!)).toEqual([255, 254, 252])
  })

  it('paints a hex-stated palette background just as well', () => {
    const variant = variantById('nord-dark')
    const { container } = render(() => <ThemeSwatch variant={variant} />)
    const chip = container.firstElementChild as HTMLElement

    expect(parseColor(chip.style.backgroundColor))
      .toEqual(parseColor(variant.palette['--background']!))
  })

  it('never draws the background or a text colour as a pip', () => {
    const variant = variantById('default-light')
    const { container } = render(() => <ThemeSwatch variant={variant} />)
    const fills = pipFills(container)

    expect(fills).not.toContain(variant.palette['--background'])
    for (const token of SWATCH_TOKENS)
      expect(token.endsWith('-foreground')).toBe(false)
  })

  it('previews each variant with its own palette', () => {
    const dark = variantById('default-dark')
    const light = variantById('default-light')
    const { container } = render(() => <ThemeSwatch variant={dark} />)

    expect(pipFills(container)).toEqual(SWATCH_TOKENS.map(t => dark.palette[t]))
    expect(pipFills(container)).not.toEqual(SWATCH_TOKENS.map(t => light.palette[t]))
  })

  it('stays decorative, because the option label carries the name', () => {
    const { container } = render(() => <ThemeSwatch variant={variantById('nord-dark')} />)
    expect((container.firstElementChild as HTMLElement).getAttribute('aria-hidden')).toBe('true')
  })

  it('draws exactly nine pips, in one grid', () => {
    const { container } = render(() => <ThemeSwatch variant={variantById('nord-dark')} />)
    expect(container.querySelectorAll('svg')).toHaveLength(1)
    expect(pipFills(container)).toHaveLength(PIP_GRID_PIPS)
  })

  it('sizes the grid from the chip, so the padding shows the background', () => {
    // The SVG carries no width or height of its own here: the class stretches
    // it to the chip's content box, and the chip's 1px padding is what leaves
    // a ring of palette background around the pips. An SVG that fell back to
    // its intrinsic size would sit in the corner instead.
    const { container } = render(() => <ThemeSwatch variant={variantById('nord-dark')} />)
    const svg = container.querySelector('svg')!
    expect(svg.hasAttribute('width')).toBe(false)
    expect(svg.hasAttribute('height')).toBe(false)
    expect(svg.getAttribute('class')).toBeTruthy()
  })

  it('shows a see-through pip for a token a malformed palette omits', () => {
    // `themes.test.ts` and the invariants below make this unreachable for the
    // catalogue. It is pinned because the alternative is worse than a gap: SVG
    // paints a rect with no fill BLACK, which reads as a colour the theme chose.
    const broken: ThemeVariant = {
      ...variantById('default-light'),
      palette: { ...variantById('default-light').palette, '--warning': undefined as unknown as string },
    }
    const { container } = render(() => <ThemeSwatch variant={broken} />)
    expect(pipFills(container)[SWATCH_TOKENS.indexOf('--warning')]).toBe('transparent')
  })

  it('previews the variant a theme resolves to for the polarity on screen', () => {
    // The palette menu previews an unpicked theme at the polarity showing, so
    // this is the path every option in that list takes.
    const resolved = resolveVariant(themeById('catppuccin'), undefined, 'dark')
    const { container } = render(() => <ThemeSwatch variant={resolved} />)

    expect(resolved.id).toBe('catppuccin-mocha')
    expect(pipFills(container)).toEqual(SWATCH_TOKENS.map(t => resolved.palette[t]))
  })
})

describe('themeSwatch palette invariants (every catalogue variant)', () => {
  it('covers the whole catalogue', () => {
    expect(ALL_VARIANTS.length).toBeGreaterThanOrEqual(30)
    expect(SWATCH_TOKENS).toHaveLength(PIP_GRID_PIPS)
  })

  it('states every swatch token in every variant', () => {
    for (const variant of ALL_VARIANTS) {
      for (const token of SWATCH_TOKENS) {
        expect(variant.palette[token], `${variant.id} states no ${token}`)
          .toMatch(/\S/)
      }
    }
  })

  it('keeps every pip off its own background', () => {
    for (const variant of ALL_VARIANTS) {
      const background = variant.palette['--background']!
      for (const token of SWATCH_TOKENS) {
        const gap = deltaE(variant.palette[token]!, background)
        expect(
          gap,
          `${variant.id}: ${token} is only ${gap.toFixed(1)} deltaE from --background, so the pip vanishes`,
        ).toBeGreaterThanOrEqual(MIN_BACKGROUND_DELTA_E)
      }
    }
  })

  it('never puts two look-alike colours in one row or one column', () => {
    // The request this chip answers: a row or a column that repeats a colour
    // reads as a rendering fault rather than as a palette. --card, --muted,
    // --faint and --secondary are absent from SWATCH_TOKENS because no
    // arrangement of them can clear this -- see that constant's comment.
    const lines: { name: string, tokens: readonly string[] }[] = []
    for (let i = 0; i < PIP_GRID_COLUMNS; i++) {
      lines.push({
        name: `row ${i}`,
        tokens: SWATCH_TOKENS.slice(i * PIP_GRID_COLUMNS, (i + 1) * PIP_GRID_COLUMNS),
      })
      lines.push({
        name: `column ${i}`,
        tokens: SWATCH_TOKENS.filter((_, n) => n % PIP_GRID_COLUMNS === i),
      })
    }
    expect(lines).toHaveLength(PIP_GRID_COLUMNS * 2)

    for (const variant of ALL_VARIANTS) {
      for (const line of lines) {
        for (let a = 0; a < line.tokens.length; a++) {
          for (let b = a + 1; b < line.tokens.length; b++) {
            const [x, y] = [line.tokens[a]!, line.tokens[b]!]
            const gap = deltaE(variant.palette[x]!, variant.palette[y]!)
            expect(
              gap,
              `${variant.id}: ${x} and ${y} share ${line.name} but differ by only ${gap.toFixed(1)} deltaE`,
            ).toBeGreaterThanOrEqual(MIN_NEIGHBOUR_DELTA_E)
          }
        }
      }
    }
  })

  it('keeps the two known collisions on diagonals', () => {
    // Two pairs in this set are the SAME colour in at least one variant, which
    // is what forced the arrangement. The rule above cannot catch them -- it
    // only measures pairs that share a line -- so state them by name here. A
    // future edit that moves either pair onto one row or column fails this.
    const collisions = [
      // Ayu Mirage: #ffcc66 and #ffcd66, one blue-channel step apart.
      { variant: 'ayu-mirage', a: '--primary', b: '--warning' } as const,
      // Default Dark states one value for both.
      { variant: 'default-dark', a: '--border', b: '--input' } as const,
    ]

    for (const { variant, a, b } of collisions) {
      const palette = variantById(variant).palette
      expect(deltaE(palette[a]!, palette[b]!), `${variant} no longer collides on ${a}/${b}`)
        .toBeLessThan(MIN_NEIGHBOUR_DELTA_E)

      const [x, y] = [SWATCH_TOKENS.indexOf(a), SWATCH_TOKENS.indexOf(b)]
      const row = (i: number) => Math.floor(i / PIP_GRID_COLUMNS)
      const column = (i: number) => i % PIP_GRID_COLUMNS
      expect(row(x), `${a} and ${b} share a row`).not.toBe(row(y))
      expect(column(x), `${a} and ${b} share a column`).not.toBe(column(y))
    }
  })
})
