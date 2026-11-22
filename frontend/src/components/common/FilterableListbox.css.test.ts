import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// jsdom does not execute WebKit touch scrolling or flex overflow shrinkage under
// an iOS visual viewport. Pin the CSS rules that keep FilterableListbox
// touch-scrollable and shrinkable inside a keyboard-constrained popover.

const source = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), 'FilterableListbox.css.ts'),
  'utf8',
)

function styleBlock(exportName: string): string {
  const marker = `export const ${exportName} = style({`
  const start = source.indexOf(marker)
  expect(start, `style export ${exportName}`).toBeGreaterThanOrEqual(0)
  const open = source.indexOf('{', start)
  let depth = 0
  for (let i = open; i < source.length; i++) {
    if (source[i] === '{')
      depth++
    else if (source[i] === '}' && --depth === 0)
      return source.slice(open + 1, i)
  }
  throw new Error(`unclosed style block for ${exportName}`)
}

describe('FilterableListbox styles', () => {
  it('contains vertical touch scrolling and shrinks cleanly when the host popover clamps', () => {
    const block = styleBlock('comboboxListbox')
    expect(block).toMatch(/height:\s*COMBOBOX_LISTBOX_HEIGHT/)
    expect(block).toMatch(/flex:\s*'0 1 auto'/)
    expect(block).toMatch(/minHeight:\s*0/)
    expect(block).toMatch(/overflowY:\s*'auto'/)
    expect(block).toMatch(/overflowX:\s*'hidden'/)
    expect(block).toMatch(/overscrollBehavior:\s*'contain'/)
    expect(block).toMatch(/WebkitOverflowScrolling:\s*'touch'/)
    expect(block).toMatch(/touchAction:\s*'pan-y'/)
  })

  it('keeps the filter input row pinned while the listbox shrinks', () => {
    const block = styleBlock('comboboxControl')
    expect(block).toMatch(/flexShrink:\s*0/)
  })
})
