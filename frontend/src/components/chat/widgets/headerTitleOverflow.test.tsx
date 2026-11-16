import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { headerTitleClips, useHeaderTitleOverflow } from './headerTitleOverflow'

/** The four sizes that headerTitleClips reads from one box. jsdom measures every box as 0. */
interface BoxSize {
  scrollWidth: number
  clientWidth: number
  scrollHeight: number
  clientHeight: number
}

const FITS: BoxSize = {
  scrollWidth: 100,
  clientWidth: 100,
  scrollHeight: 20,
  clientHeight: 20,
}

/** Make `element` report `size`, read at each measure, so a test can change it later. */
function stubSize(element: HTMLElement, size: () => BoxSize): void {
  for (const name of [
    'scrollWidth',
    'clientWidth',
    'scrollHeight',
    'clientHeight',
  ] as const) {
    Object.defineProperty(element, name, {
      configurable: true,
      get: () => size()[name],
    })
  }
}

/** A title box that holds `parts`, each a span with its own size. */
function titleBox(box: BoxSize, ...parts: BoxSize[]): HTMLElement {
  const element = document.createElement('span')
  stubSize(element, () => box)
  for (const size of parts) {
    const part = document.createElement('span')
    stubSize(part, () => size)
    element.append(part)
  }
  return element
}

describe('headerTitleClips', () => {
  it('answers false for a title that fits', () => {
    expect(headerTitleClips(titleBox(FITS, FITS))).toBe(false)
  })

  it('answers true for a title wider than its box', () => {
    expect(headerTitleClips(titleBox({ ...FITS, scrollWidth: 240 }))).toBe(true)
  })

  // The box has the height of one line, so a part that wraps onto a second line is cut off.
  it('answers true for a title that wrapped onto a second line', () => {
    expect(headerTitleClips(titleBox({ ...FITS, scrollHeight: 40 }))).toBe(true)
  })

  // A part that ends in an ellipsis clips inside its own box, and the outer box fits.
  it('answers true for a part that clips inside its own box', () => {
    expect(headerTitleClips(titleBox(FITS, FITS, { ...FITS, scrollWidth: 300, clientWidth: 80 }))).toBe(true)
  })

  // An inline part has no client width, and no clip either.
  it('ignores an inline part, which has no box of its own', () => {
    expect(headerTitleClips(titleBox(FITS, { ...FITS, scrollWidth: 300, clientWidth: 0 }))).toBe(false)
  })

  it('ignores a difference below half a pixel', () => {
    expect(headerTitleClips(titleBox({ ...FITS, scrollWidth: 100.4 }))).toBe(false)
  })

  // A title mixes a proportional and a monospace face on one baseline, and the browser
  // rounds the content height of that line, so a title that fits can read one pixel
  // taller than its box. A part that broke onto a second line adds a whole line.
  it('ignores a content height one pixel taller than the box', () => {
    expect(headerTitleClips(titleBox({ ...FITS, scrollHeight: 21 }))).toBe(false)
    expect(headerTitleClips(titleBox({ ...FITS, scrollHeight: 22 }))).toBe(true)
  })
})

describe('useHeaderTitleOverflow', () => {
  /** Mount a title box whose size and words the test drives, and record each report. */
  function mountTitle(initial: BoxSize, opts: { expanded?: boolean } = {}) {
    const [size, setSize] = createSignal(initial)
    const [words, setWords] = createSignal('Read src/main.ts')
    const [expanded, setExpanded] = createSignal(opts.expanded ?? false)
    const reports: boolean[] = []
    let overflowing: () => boolean = () => false
    function Title() {
      const overflow = useHeaderTitleOverflow({
        expanded,
        onOverflowChange: next => reports.push(next),
      })
      overflowing = overflow.overflowing
      // The size is stubbed before the box reaches the hook, so the first measure reads it.
      return (
        <span ref={(element) => {
          stubSize(element, size)
          overflow.ref(element)
        }}
        >
          {words()}
        </span>
      )
    }
    render(() => <Title />)
    return {
      reports,
      overflowing: () => overflowing(),
      setSize,
      setWords,
      setExpanded,
    }
  }

  /** Let the mutation observer deliver its records. */
  const flushMutations = () => new Promise<void>(resolve => queueMicrotask(resolve))

  it('reports a collapsed title that clips when it mounts', () => {
    const title = mountTitle({ ...FITS, scrollWidth: 240 })
    expect(title.reports).toEqual([true])
    expect(title.overflowing()).toBe(true)
  })

  it('reports nothing for a title that fits', () => {
    const title = mountTitle(FITS)
    expect(title.reports).toEqual([])
    expect(title.overflowing()).toBe(false)
  })

  // A request row's title gains the words of its result in place, with no resize.
  it('measures again when the words of the title change in place', async () => {
    const title = mountTitle(FITS)
    title.setSize({ ...FITS, scrollWidth: 240 })
    title.setWords('Read src/main.ts (412 lines)')
    await flushMutations()
    expect(title.reports).toEqual([true])
  })

  // An expanded title wraps and clips nothing. Reporting false then would remove the
  // fact that made the row expandable, and the control would leave the expanded row.
  it('keeps the collapsed answer while the title is expanded', async () => {
    const title = mountTitle({ ...FITS, scrollWidth: 240 })
    title.setExpanded(true)
    title.setSize(FITS)
    title.setWords('Read src/main.ts (412 lines)')
    await flushMutations()
    expect(title.reports).toEqual([true])
    expect(title.overflowing()).toBe(true)

    title.setExpanded(false)
    expect(title.reports).toEqual([true, false])
    expect(title.overflowing()).toBe(false)
  })

  // The expand state of a row outlives a remount (see createMessageUiState). A row that
  // mounts expanded has no collapsed answer yet, and a clipped title can be the one reason
  // that the row is expandable: the row keeps its control until it collapses and measures.
  it('reports a clip for a title that mounts expanded, until the title collapses', () => {
    const title = mountTitle(FITS, { expanded: true })
    expect(title.reports).toEqual([true])
    expect(title.overflowing()).toBe(true)

    title.setExpanded(false)
    expect(title.reports).toEqual([true, false])
    expect(title.overflowing()).toBe(false)
  })

  it('stops measuring when the title unmounts', async () => {
    const disconnect = vi.spyOn(MutationObserver.prototype, 'disconnect')
    const [shown, setShown] = createSignal(true)
    function Title() {
      const overflow = useHeaderTitleOverflow({
        expanded: () => false,
      })
      return <span ref={overflow.ref}>words</span>
    }
    render(() => (
      <>
        {shown()
          ? <Title />
          : null}
      </>
    ))
    setShown(false)
    await flushMutations()
    expect(disconnect).toHaveBeenCalled()
    disconnect.mockRestore()
  })
})
