import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { terminalWrapper } from './TerminalView.css'

/**
 * The terminal viewport background is a SOURCE CONTRACT plus a real cascade
 * over the two rule texts, in the shape of the dialog module's own test.
 *
 * The runner stubs CSS imports (vitest's `css` option is off), so this
 * document never sees the module's emitted styles and `getComputedStyle`
 * cannot resolve them. Turning CSS processing on for the suite is not the
 * way out: it flips the computed-style baseline that the tooltip-clipping
 * tests stand on, and seven of those files fail. So the test renders the two
 * rule texts itself -- the override's selector and value read out of the
 * module source, xterm.css's rule out of the installed dependency -- and
 * lets jsdom resolve the real cascade on them.
 *
 * What regressed once already: xterm.css hardcodes an opaque `#000` on the
 * viewport, the override neutralizes it, and the two rules tied on
 * specificity. The tie fell to the stylesheet the bundler emitted LAST,
 * which is not an order this project chooses. The override must therefore
 * win on specificity alone.
 */

const moduleSource = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), 'TerminalView.css.ts'),
  'utf8',
)

const xtermCss = readFileSync(
  createRequire(import.meta.url).resolve('@xterm/xterm/css/xterm.css'),
  'utf8',
)

/**
 * The viewport background rule the installed xterm.css actually ships: the
 * competitor the override exists to beat. Undefined once xterm stops
 * declaring one.
 */
function xtermViewportBackgroundRule(): { selector: string, value: string } | undefined {
  for (const [, rawSelector, body] of xtermCss.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    if (!rawSelector || !body)
      continue
    const selector = rawSelector
      .replace(/\/\*[\s\S]*?\*\//g, ' ')
      .replace(/\s+/g, ' ')
      .trim()
    const value = /background-color\s*:\s*([^;]+)/i.exec(body)?.[1]?.trim()
    if (selector.includes('.xterm-viewport') && value)
      return { selector, value }
  }
  return undefined
}

/**
 * The `globalStyle` call in the module source that neutralizes the
 * competitor: its selector is `${terminalWrapper}` plus xterm.css's own.
 * Assembled so no plain string literal holds a `${` -- the dialog test uses
 * the same escape for eslint's no-template-curly-in-string.
 */
function overrideCall(): string {
  const rule = xtermViewportBackgroundRule()
  expect(rule, 'xterm.css hardcodes no viewport background, so nothing needs the override').toBeDefined()
  const marker = `globalStyle(\`$${''}{terminalWrapper} ${rule!.selector}\``
  const start = moduleSource.indexOf(marker)
  expect(
    start,
    'the viewport override must mirror xterm.css\'s viewport selector under the wrapper, '
    + `one class wider: ${marker}`,
  ).toBeGreaterThanOrEqual(0)
  const next = moduleSource.indexOf('globalStyle(', start + marker.length)
  const end = next < 0
    ? moduleSource.length
    : next
  return moduleSource.slice(start, end)
}

/** The viewport DOM the two rules fight over, under the real wrapper class. */
function terminalViewport(xtermClasses = 'xterm'): { viewport: HTMLElement, wrapper: HTMLElement } {
  const wrapper = document.createElement('div')
  wrapper.className = terminalWrapper
  const xterm = document.createElement('div')
  xterm.className = xtermClasses
  const viewport = document.createElement('div')
  viewport.className = 'xterm-viewport'
  xterm.append(viewport)
  wrapper.append(xterm)
  document.body.append(wrapper)
  return { viewport, wrapper }
}

/**
 * Resolve `fn` with exactly these stylesheet texts live, in the given order,
 * on a viewport whose `.xterm` carries `xtermClasses`.
 */
function withStylesheets(
  texts: string[],
  fn: (viewport: HTMLElement) => void,
  xtermClasses = 'xterm',
): void {
  const tags = texts.map((text) => {
    const style = document.createElement('style')
    style.textContent = text
    document.head.append(style)
    return style
  })
  const { viewport, wrapper } = terminalViewport(xtermClasses)
  try {
    fn(viewport)
  }
  finally {
    for (const tag of tags)
      tag.remove()
    wrapper.remove()
  }
}

describe('terminal viewport background (TerminalView.css.ts)', () => {
  it('anchors on the viewport background the installed xterm.css hardcodes', () => {
    const rule = xtermViewportBackgroundRule()
    expect(rule, 'xterm.css hardcodes no viewport background').toBeDefined()
    // The override mirrors this exact selector. When xterm moves the rule,
    // this fails and asks for the mirror to be re-derived.
    expect(rule!.selector).toBe('.xterm:not(.allow-transparency) .xterm-viewport')
    // The competitor is OPAQUE, and that is the whole reason the override
    // exists. If xterm ever makes it transparent, re-examine the override.
    expect(rule!.value, 'xterm.css\'s viewport background is no longer opaque').not.toMatch(/transparent|none/i)
  })

  it('neutralizes it as a transparent background on the mirrored selector', () => {
    // Transparent, not a colour: xterm paints the terminal theme's background
    // inline on `.xterm` (Viewport.ts), and the viewport must not cover it.
    expect(overrideCall()).toMatch(/backgroundColor:\s*['"]transparent['"]/)
  })

  it('wins the cascade in either stylesheet order', () => {
    const rule = xtermViewportBackgroundRule()!
    const value = /backgroundColor:\s*['"]([^'"]+)['"]/.exec(overrideCall())![1]
    // `style()` yields a bare class name; vanilla-extract compiles the
    // interpolated selector to `.class`, which is what this renders too.
    const override = `.${terminalWrapper} ${rule.selector} { background-color: ${value}; }`

    // The control: xterm.css's rule alone paints the viewport black. This is
    // the bug the override removes, and the proof that the cascade below
    // measures anything at all.
    withStylesheets([`${rule.selector} { background-color: ${rule.value}; }`], (viewport) => {
      expect(getComputedStyle(viewport).backgroundColor).toBe('rgb(0, 0, 0)')
    })

    for (const texts of [
      [`${rule.selector} { background-color: ${rule.value}; }`, override],
      [override, `${rule.selector} { background-color: ${rule.value}; }`],
    ]) {
      withStylesheets(texts, (viewport) => {
        expect(getComputedStyle(viewport).backgroundColor).toBe('rgba(0, 0, 0, 0)')
      })
    }
  })

  it('paints no viewport background on a terminal that asked for transparency', () => {
    // The scoping boundary of both rules. A transparency terminal (the quake
    // panel) carries `allow-transparency`, so xterm.css's `:not()` rule and
    // the override's mirror both skip it: nothing paints the viewport, and
    // the panel behind it shows through.
    const rule = xtermViewportBackgroundRule()!
    const xtermRule = `${rule.selector} { background-color: ${rule.value}; }`
    const value = /backgroundColor:\s*['"]([^'"]+)['"]/.exec(overrideCall())![1]
    const override = `.${terminalWrapper} ${rule.selector} { background-color: ${value}; }`
    const transparentTerminal = 'xterm allow-transparency'

    // The control: xterm.css's black never reaches such a terminal either.
    withStylesheets([xtermRule], (viewport) => {
      expect(getComputedStyle(viewport).backgroundColor).toBe('rgba(0, 0, 0, 0)')
    }, transparentTerminal)

    for (const texts of [
      [xtermRule, override],
      [override, xtermRule],
    ]) {
      withStylesheets(texts, (viewport) => {
        expect(getComputedStyle(viewport).backgroundColor).toBe('rgba(0, 0, 0, 0)')
      }, transparentTerminal)
    }
  })

  it('keeps the native scrollbar hidden on every viewport, transparent included', () => {
    // The background override is scoped to `:not(.allow-transparency)`, which
    // is exactly the set xterm.css paints black. This half is not scoped:
    // xterm.css declares `overflow-y: scroll` on every viewport, so the quake
    // panel still needs the native bar hidden.
    expect(moduleSource).toContain('hideNativeScrollbar(`$' + '{terminalWrapper} .xterm .xterm-viewport`)')
    expect(moduleSource).toMatch(/`\$\{terminalWrapper\} \.xterm \.xterm-viewport`[\s\S]{0,120}?scrollbarWidth:\s*'none'/)
  })
})
