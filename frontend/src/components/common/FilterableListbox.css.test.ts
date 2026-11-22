import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { touchScrollContain } from '~/styles/popover.css'
import { extractStyleCallBlock } from '~/test-support/styleSource'
import { comboboxListbox } from './FilterableListbox.css'

// jsdom does not execute WebKit touch scrolling or flex overflow shrinkage under
// an iOS visual viewport. Pin the CSS rules that keep FilterableListbox
// touch-scrollable and shrinkable inside a keyboard-constrained popover.

const source = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), 'FilterableListbox.css.ts'),
  'utf8',
)

describe('FilterableListbox styles', () => {
  it('contains vertical touch scrolling and shrinks cleanly when the host popover clamps', () => {
    const block = extractStyleCallBlock(source, 'comboboxListbox')
    expect(block).toMatch(/height:\s*COMBOBOX_LISTBOX_HEIGHT/)
    expect(block).toMatch(/flex:\s*'0 1 auto'/)
    expect(block).toMatch(/minHeight:\s*0/)
    expect(block).toMatch(/overflowY:\s*'auto'/)
    expect(block).toMatch(/overflowX:\s*'hidden'/)
    expect(block).toMatch(/touchAction:\s*'pan-y'/)
    expect(comboboxListbox.split(' ')).toContain(touchScrollContain)
  })

  it('keeps the filter input row pinned while the listbox shrinks', () => {
    const block = extractStyleCallBlock(source, 'comboboxControl')
    expect(block).toMatch(/flexShrink:\s*0/)
  })

  it('raises the filter input font size to 16px on narrow screens to prevent iOS Safari auto-zoom', () => {
    const block = extractStyleCallBlock(source, 'comboboxInput')
    expect(block).toMatch(/breakpoints\.sm\s*-\s*1/)
    expect(block).toMatch(/fontSize:\s*'16px'/)
  })

  it('truncates long option labels inside flex rows so trailing indicators stay visible', () => {
    const block = extractStyleCallBlock(source, 'comboboxItemLabel')
    expect(block).toMatch(/minWidth:\s*0/)
    expect(block).toMatch(/overflow:\s*'hidden'/)
    expect(block).toMatch(/textOverflow:\s*'ellipsis'/)
    expect(block).toMatch(/whiteSpace:\s*'nowrap'/)
  })
})
