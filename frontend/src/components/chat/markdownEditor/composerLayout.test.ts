import { createRoot } from 'solid-js'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { createComposerLayout } from './composerLayout'

/**
 * jsdom lays nothing out, so every measurement reads 0. These stubs give the
 * probe and the row real numbers: the text probe reports 10px per character,
 * and the row reports whatever width the test asks for. That is enough to drive
 * the whole decision, which is the point of extracting it from the component —
 * the thresholds and the hysteresis are now reachable without mounting Milkdown.
 *
 * The width stub goes on `HTMLSpanElement`, which is exactly the text probe:
 * the layout's other probe is a `<div>`, and it is read through
 * `getComputedStyle`, not `offsetWidth`.
 */
const CHAR_PX = 10

beforeAll(() => {
  Object.defineProperty(HTMLSpanElement.prototype, 'offsetWidth', {
    get(this: HTMLSpanElement) {
      return (this.textContent ?? '').length * CHAR_PX
    },
    configurable: true,
  })
})

afterAll(() => {
  // Removing the shadow restores HTMLElement.prototype's own accessor, which is
  // where jsdom declares `offsetWidth`.
  Reflect.deleteProperty(HTMLSpanElement.prototype, 'offsetWidth')
})

function stubbedRoot(): HTMLElement {
  return document.createElement('div')
}

function stubbedRow(width: number): HTMLElement {
  const row = document.createElement('div')
  Object.defineProperty(row, 'clientWidth', { get: () => width, configurable: true })
  return row
}

/** Build a layout over stubbed DOM and run `fn` with it inside a reactive root. */
function withLayout(
  opts: { rowWidth?: number, observe?: boolean },
  fn: (layout: ReturnType<typeof createComposerLayout>) => void,
) {
  document.body.replaceChildren()
  const root = stubbedRoot()
  document.body.appendChild(root)
  const row = opts.rowWidth == null ? undefined : stubbedRow(opts.rowWidth)
  if (row)
    document.body.appendChild(row)

  createRoot((dispose) => {
    const layout = createComposerLayout({
      editorRoot: () => root,
      row: () => row,
      actionSlot: () => undefined,
      // No rendered block: the layout falls back to the classifier's plain text,
      // which is the pre-mount path and keeps this test independent of Milkdown.
      firstBlock: () => undefined,
    })
    if (opts.observe !== false)
      layout.observe()
    fn(layout)
    dispose()
  })
}

describe('createComposerLayout', () => {
  it('expands for multi-line content whatever its width', () => {
    withLayout({ rowWidth: 1000 }, (layout) => {
      expect(layout.contentExpanded()).toBe(false)
      layout.setDocStats({ multiLine: true, text: '' })
      expect(layout.contentExpanded()).toBe(true)
    })
  })

  it('stays collapsed while the row is not measured yet', () => {
    // An unmeasured row reads as unlimited width, so the box starts collapsed
    // rather than flashing expanded before the first measurement lands.
    withLayout({ rowWidth: undefined }, (layout) => {
      layout.setDocStats({ multiLine: false, text: 'x'.repeat(500) })
      expect(layout.contentExpanded()).toBe(false)
    })
  })

  it('stays collapsed while the text fits the collapsed width', () => {
    // Available width = 1000 - leftPad(0) - rightPad(96) = 904; the expand
    // margin is 16, so 50 characters (500px) is comfortably inside.
    withLayout({ rowWidth: 1000 }, (layout) => {
      layout.setDocStats({ multiLine: false, text: 'x'.repeat(50) })
      expect(layout.contentExpanded()).toBe(false)
    })
  })

  it('expands once the text would wrap at the collapsed width', () => {
    withLayout({ rowWidth: 1000 }, (layout) => {
      layout.setDocStats({ multiLine: false, text: 'x'.repeat(100) })
      expect(layout.contentExpanded()).toBe(true)
    })
  })

  it('collapses later than it expands, so a borderline string cannot oscillate', () => {
    // Available width 904. Expand threshold 904-16 = 888 (89 chars). Collapse
    // threshold 904-32 = 872 (88 chars). A string between the two must KEEP the
    // expanded layout it already has: the mode switch itself changes the
    // measured width, so equal thresholds would flip back and forth forever.
    withLayout({ rowWidth: 1000 }, (layout) => {
      layout.setDocStats({ multiLine: false, text: 'x'.repeat(100) })
      expect(layout.contentExpanded()).toBe(true)

      // 88 chars = 880px: past the collapse threshold, inside the expand one.
      layout.setDocStats({ multiLine: false, text: 'x'.repeat(88) })
      expect(layout.contentExpanded()).toBe(true)

      // 87 chars = 870px: below both, so it finally collapses.
      layout.setDocStats({ multiLine: false, text: 'x'.repeat(87) })
      expect(layout.contentExpanded()).toBe(false)
    })
  })

  it('measures the RENDERED block, not the classifier\'s plain text', () => {
    // The whole point of cloning. `stats.text` here is 10 characters (100px),
    // comfortably inside the 904px available width — but the rendered block is
    // 100 characters wide. Measuring the text would keep the box collapsed and
    // let the real content wrap under the overlaid buttons.
    document.body.replaceChildren()
    const root = stubbedRoot()
    const row = stubbedRow(1000)
    document.body.append(root, row)

    const block = document.createElement('p')
    block.append(document.createTextNode('x'.repeat(100)))

    createRoot((dispose) => {
      const layout = createComposerLayout({
        editorRoot: () => root,
        row: () => row,
        actionSlot: () => undefined,
        firstBlock: () => block,
      })
      layout.observe()
      layout.setDocStats({ multiLine: false, text: 'ten chars.' })

      expect(layout.contentExpanded()).toBe(true)
      dispose()
    })
  })

  it('reports the unmeasured right padding until the action slot is measured', () => {
    withLayout({ rowWidth: 1000 }, (layout) => {
      expect(layout.rightPad()).toBe(96)
      // Zero means "not measured", which is how the stylesheet knows to keep
      // its own one-button-height fallback for the row reservation.
      expect(layout.actionsHeight()).toBe(0)
    })
  })

  it('removes its probes when the owner disposes', () => {
    document.body.replaceChildren()
    const root = stubbedRoot()
    const row = stubbedRow(1000)
    document.body.append(root, row)

    const dispose = createRoot((d) => {
      const layout = createComposerLayout({
        editorRoot: () => root,
        row: () => row,
        actionSlot: () => undefined,
        firstBlock: () => undefined,
      })
      layout.observe()
      layout.setDocStats({ multiLine: false, text: 'measure me' })
      return d
    })

    expect(root.childElementCount).toBe(1)
    expect(row.childElementCount).toBe(1)
    dispose()
    expect(root.childElementCount).toBe(0)
    expect(row.childElementCount).toBe(0)
  })
})
