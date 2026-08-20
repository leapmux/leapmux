import { readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { collectStyleFiles } from '~/test-support/styleFiles'

// A highlighted surface is themed by TWO rules that must land on the same
// element, and nothing in the type system ties them:
//
//   - `shikiDualThemeColors` paints the token spans. Shiki bakes each token's
//     colour in at tokenize time, from the SYNTAX theme.
//   - `codeSurfaceTheme` paints what those tokens land on, from the same
//     syntax variant, and repoints --danger/--border/--faint-foreground on the
//     subtree so the diff tints and line numbers follow too.
//
// Apply the first alone and the surface keeps the APP's palette. That is
// invisible while the two themes share a polarity, and unreadable the moment
// they do not: a dark syntax theme's tokens on a light page measure a median
// 1.97:1, and 1.53:1 on a diff row.
//
// `codeSurface` is now the one door, and it DERIVES each token selector from the
// surface, so the two rules cannot land on different elements. This guard is
// what keeps that door the only one: a file that reaches for a primitive
// directly could still pair them by hand on two different selectors, which a
// text scan of a FILE cannot see.

const srcRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
/** Publishes `--code-*` from the resolved syntax variant. */
const PUBLISHER = join(srcRoot, 'styles', 'global.css.ts')

/** The module that DEFINES the helpers, and so mentions them without applying them. */
const DEFINITION = join(srcRoot, 'components', 'chat', 'shikiTokenColors.css.ts')

describe('code surfaces are paired with their token colours', () => {
  it('routes every code surface through the one door', () => {
    // The primitives are unexported, so this cannot be broken by an import --
    // but a file inside the defining module's own directory could still reach
    // them. Pairing by hand on two DIFFERENT selectors is the drift a per-file
    // text scan is blind to, and `codeSurface` makes it unwritable by deriving
    // the token selectors from the surface.
    const direct: string[] = []
    for (const file of collectStyleFiles(srcRoot)) {
      if (file === DEFINITION)
        continue
      const source = readFileSync(file, 'utf8')
      if (source.includes('codeSurfaceTheme(') || source.includes('shikiDualThemeColors('))
        direct.push(relative(srcRoot, file))
    }
    expect(direct, `these call a code-surface primitive directly; use codeSurface() so the selectors cannot separate:\n  ${direct.join('\n  ')}`)
      .toEqual([])
  })

  it('finds the surfaces it is meant to be guarding', () => {
    // Without this the case above passes vacuously the day the helper is
    // renamed, which is exactly when it needs to fail.
    const paired = collectStyleFiles(srcRoot)
      .filter(f => f !== DEFINITION && readFileSync(f, 'utf8').includes('codeSurface('))
    expect(paired.length, 'no file declares a code surface -- has the helper been renamed?')
      .toBeGreaterThanOrEqual(6)
  })
})

describe('the code palette is published where it is read', () => {
  it('publishes every --code-* token a code surface reads', () => {
    // Two files, one contract, and nothing between them: `codeSurfaceTheme`
    // repoints the app's token names at `--code-*`, and the `data-code-variant`
    // loop in global.css.ts is what puts a value there. A name that only one
    // side knows resolves to nothing -- `background-color: var(--code-card)`
    // with no `--code-card` is an invalid declaration, so the surface silently
    // keeps whatever it inherited. That is the same failure this file's other
    // guard exists for, one layer down.
    // Read from EVERY style file, not just the one that defines the helper:
    // `codeSurfaceTheme` repoints the app's own token names, and a surface may
    // also read a `--code-*` directly when there is no app token to repoint --
    // `diffStyles` does exactly that for the two tint strengths.
    const read = new Set(collectStyleFiles(srcRoot)
      .filter(f => f !== PUBLISHER)
      .flatMap(f => [...readFileSync(f, 'utf8').matchAll(/var\((--code-[a-z-]+)\)/g)].map(m => m[1]!)))
    const published = new Set([...readFileSync(PUBLISHER, 'utf8').matchAll(/'(--code-[a-z-]+)':/g)].map(m => m[1]!))

    expect(read.size, 'no --code-* token is read -- has codeSurface been renamed?').toBeGreaterThanOrEqual(9)
    const missing = [...read].filter(t => !published.has(t)).sort()
    expect(missing, `read by a code surface but never published:\n  ${missing.join('\n  ')}`).toEqual([])
    const unused = [...published].filter(t => !read.has(t)).sort()
    expect(unused, `published but read by nothing:\n  ${unused.join('\n  ')}`).toEqual([])
  })

  it('publishes a fallback that cannot outrank a variant rule', () => {
    // `:root` is a pseudo-class, so it scores (0,1,0) -- the SAME as
    // `[data-code-variant="X"]`, and both match <html>. The fallback declared as
    // `:root` AFTER the loop therefore won every contest by declaration order:
    // the code palette stayed Default-light under all thirty variants, so a code
    // block took the page's own colour on the default theme and became a white
    // slab with dark text on every dark one. Nothing failed; it just painted the
    // wrong palette.
    //
    // Specificity was the first repair and it is no longer enough. The fallback
    // needs a DARK half (see the case below), and picking a polarity needs
    // `[data-theme="dark"]`, which takes `html[data-theme="dark"]` to (0,1,1) --
    // above every variant rule, reinstating the defect in the dark. So the
    // mechanism is now a rule that stops MATCHING once `data-code-variant`
    // lands, which needs no precedence argument and holds for whatever selector
    // each half requires.
    const source = readFileSync(PUBLISHER, 'utf8')
    // `codeVars(` only, which is the call that PUBLISHES a variant's code
    // palette. The UI fallback also names `--code-block-background` and
    // `--code-card`, but those are DERIVED -- a `var(--code-*)` expression that
    // resolves against whichever palette rule won on <html> -- so one
    // declaration correctly answers for all thirty variants and for the
    // fallback, and guarding it would be wrong.
    const codeFallbacks = [...source.matchAll(/globalStyle\(\s*'([^']*)'\s*,\s*\{([\s\S]*?)\n\}\)/g)]
      .filter(m => m[2]!.includes('codeVars('))
      .map(m => m[1]!)
      .filter(sel => !sel.includes('[data-code-variant="'))

    expect(codeFallbacks.length, 'no whole-document rule publishes the code palette -- has the fallback moved?')
      .toBeGreaterThanOrEqual(2)
    for (const selector of codeFallbacks) {
      expect(
        selector,
        `\`${selector}\` publishes the code palette without :not([data-code-variant]), so it can outrank a variant rule`,
      ).toContain(':not([data-code-variant])')
    }
  })

  it('publishes BOTH polarities, so a dark app is not painted light', () => {
    // `data-code-variant` is written only after `setSyntaxTheme` resolves, and
    // for any of the 29 non-default variants that awaits a real
    // `@shikijs/themes/*` chunk import. The delay is deliberate -- it stops the
    // SURFACE repainting ahead of the tokens that land on it -- so the fallback
    // governs a real window on every load rather than one frame.
    //
    // `~/lib/themeStore` writes `data-theme` synchronously at module import, so
    // the app is ALREADY dark at first paint. A light-only fallback therefore
    // painted every diff, Read view, tool body and fenced block in a dark app as
    // a near-white slab for the whole import, then flipped.
    const source = readFileSync(PUBLISHER, 'utf8')
    for (const polarity of ['light', 'dark']) {
      expect(
        source,
        `the code fallback must publish the ${polarity} half`,
      ).toContain(`codeVars(resolveVariant(defaultTheme, undefined, '${polarity}'))`)
    }
  })
})
