import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// Modal <dialog> lives in the top layer, so it escapes body's
// `padding-top: env(safe-area-inset-top)`. The panel must re-declare the
// safe-area insets itself; without that, the close button sits under the
// status bar / Dynamic Island in an iOS standalone PWA and is untappable.
//
// jsdom cannot resolve env(safe-area-inset-*) and Playwright's Chromium
// reports 0 on desktop, so this is a source contract: the declarations
// that fix the bug must stay wired on the :modal rule in Dialog.css.ts.

const source = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), 'Dialog.css.ts'),
  'utf8',
)

/** Body of the dialog standard :modal globalStyle object literal. */
function standardModalBlock(): string {
  // Assemble without a literal `${` so eslint's no-template-curly-in-string
  // stays quiet; indexOf('{') must start AFTER that template brace.
  const marker = `globalStyle(\`dialog.$${''}{standard}:modal\``
  const start = source.indexOf(marker)
  expect(start, 'dialog standard :modal globalStyle').toBeGreaterThanOrEqual(0)
  const open = source.indexOf('{', start + marker.length)
  expect(open, 'object literal after :modal selector').toBeGreaterThan(start)
  let depth = 0
  for (let i = open; i < source.length; i++) {
    if (source[i] === '{')
      depth++
    else if (source[i] === '}' && --depth === 0)
      return source.slice(open + 1, i)
  }
  throw new Error('unclosed dialog standard :modal block')
}

describe('dialog safe-area insets (Dialog.css.ts)', () => {
  it('defines each inset as env() with a test-only CSS-variable override', () => {
    for (const inset of [
      'safe-area-inset-top',
      'safe-area-inset-right',
      'safe-area-inset-bottom',
      'safe-area-inset-left',
    ]) {
      expect(source, inset).toContain(
        `var(--leapmux-${inset}, env(${inset}, 0px))`,
      )
    }
  })

  it('wires SAFE_* into top/right/bottom/left on the :modal rule that beats UA inset:0', () => {
    // `dialog.${standard}:modal` outranks `dialog:modal { inset: 0 }`. A rule
    // on `.standard` alone loses that fight and the panel stays full-bleed.
    expect(source).toMatch(/dialog\.\$\{standard\}:modal/)

    const block = standardModalBlock()
    // Property wiring — not merely that the env() strings exist somewhere.
    expect(block).toMatch(/['"]top['"]\s*:\s*SAFE_TOP\b/)
    expect(block).toMatch(/['"]right['"]\s*:\s*SAFE_RIGHT\b/)
    expect(block).toMatch(/['"]bottom['"]\s*:\s*SAFE_BOTTOM\b/)
    expect(block).toMatch(/['"]left['"]\s*:\s*SAFE_LEFT\b/)
    expect(block).toMatch(/['"]height['"]\s*:\s*['"]fit-content['"]/)
  })

  it('lets tall and huge dialogs fill the safe rectangle on the phone band', () => {
    expect(source).toMatch(/dialog\.\$\{standard\}\.\$\{tall\}:modal/)
    expect(source).toMatch(/dialog\.\$\{standard\}\.\$\{huge\}:modal/)
    expect(source).toContain('SAFE_MAX_HEIGHT')
  })
})
