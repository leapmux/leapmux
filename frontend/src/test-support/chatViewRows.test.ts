import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { measureHiddenCopies, reportChatListSize } from './chatViewRows'
import { installControllableResizeObserver } from './resizeObserverStub'

let originalResizeObserver: typeof ResizeObserver | undefined

beforeEach(() => {
  originalResizeObserver = globalThis.ResizeObserver
  installControllableResizeObserver()
})

afterEach(() => {
  vi.restoreAllMocks()
  document.body.replaceChildren()
  if (originalResizeObserver)
    globalThis.ResizeObserver = originalResizeObserver
  else
    Reflect.deleteProperty(globalThis, 'ResizeObserver')
})

/** A container with one premeasure root that holds `copies` hidden copies. */
function containerWithCopies(copies: number): { container: HTMLElement, root: HTMLElement } {
  const container = document.createElement('div')
  const root = document.createElement('div')
  root.dataset.chatPremeasureRoot = 'true'
  for (let index = 0; index < copies; index++)
    root.append(document.createElement('div'))
  container.append(root)
  document.body.append(container)
  return { container, root }
}

describe('measureHiddenCopies', () => {
  it('gives each hidden copy a layout box of the stated height, and reports it to its observer', async () => {
    const { container, root } = containerWithCopies(2)
    const seen: number[] = []
    // The app removes a copy when its height is committed. This observer does the same.
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        seen.push(entry.target.getBoundingClientRect().height)
        entry.target.remove()
      }
    })
    for (const copy of root.children)
      observer.observe(copy)

    await measureHiddenCopies(container, () => 40)

    expect(seen).toEqual([40, 40])
    expect(root.childElementCount).toBe(0)
  })

  it('returns at once for a container with no hidden copy', async () => {
    const { container } = containerWithCopies(0)

    await expect(measureHiddenCopies(container)).resolves.toBeUndefined()
  })

  it('throws when a hidden copy never reports, so that no test reads the copy for the row', async () => {
    // No observer watches the copy, as for a copy whose content never becomes measurable.
    const { container } = containerWithCopies(1)

    await expect(measureHiddenCopies(container)).rejects.toThrow('1 hidden copies report no height after 20 rounds')
  })
})

describe('reportChatListSize', () => {
  /** A container with a scroll container and a premeasure root that holds one hidden copy. */
  function containerWithList(): { container: HTMLElement, scrollContainer: HTMLElement, root: HTMLElement } {
    const { container, root } = containerWithCopies(1)
    const scrollContainer = document.createElement('div')
    scrollContainer.dataset.chatScrollContainer = 'true'
    container.append(scrollContainer)
    return { container, scrollContainer, root }
  }

  /** Observe `target`, and collect the size that each report gives. */
  function collectSizes(target: HTMLElement): { width: number, height: number }[] {
    const sizes: { width: number, height: number }[] = []
    new ResizeObserver((entries) => {
      for (const entry of entries)
        sizes.push({ width: entry.contentRect.width, height: entry.contentRect.height })
    }).observe(target)
    return sizes
  }

  it('reports the stated size to the observer of the scroll container, and returns the container', async () => {
    const { container, scrollContainer } = containerWithList()
    const sizes = collectSizes(scrollContainer)

    const returned = await reportChatListSize({ container }, 640, 500)

    expect(returned).toBe(scrollContainer)
    expect(sizes).toEqual([
      { width: 640, height: 500 },
    ])
  })

  it('reports 800 by 733 when it gets no size', async () => {
    const { container, scrollContainer } = containerWithList()
    const sizes = collectSizes(scrollContainer)

    await reportChatListSize({ container })

    expect(sizes).toEqual([
      { width: 800, height: 733 },
    ])
  })

  it('gives the scroll container the client height of the reported size, before its observer runs', async () => {
    const { container, scrollContainer } = containerWithList()
    const seen: number[] = []
    // The scroll hook reads the client height when its observer reports.
    new ResizeObserver(() => seen.push(scrollContainer.clientHeight)).observe(scrollContainer)

    await reportChatListSize({ container }, 640, 500)
    await reportChatListSize({ container }, 640, 0)
    await reportChatListSize({ container })

    // The second report is a pane that lost its layout box.
    expect(seen).toEqual([500, 0, 733])
  })

  it('keeps a client height that the test defined before the report', async () => {
    const { container, scrollContainer } = containerWithList()
    Object.defineProperty(scrollContainer, 'clientHeight', {
      configurable: true,
      get: () => 200,
    })

    await reportChatListSize({ container }, 640, 500)

    expect(scrollContainer.clientHeight).toBe(200)
  })

  it('lets a test replace the client height after a report, and keeps the replacement at a later report', async () => {
    const { container, scrollContainer } = containerWithList()
    await reportChatListSize({ container }, 640, 500)

    Object.defineProperty(scrollContainer, 'clientHeight', {
      configurable: true,
      get: () => 200,
    })
    await reportChatListSize({ container }, 640, 733)

    expect(scrollContainer.clientHeight).toBe(200)
  })

  it('leaves the hidden copies unmeasured', async () => {
    const { container, root } = containerWithList()
    const copy = root.firstElementChild!
    const sizes = collectSizes(copy as HTMLElement)

    await reportChatListSize({ container })

    expect(sizes).toEqual([])
    expect(copy.getBoundingClientRect().height).toBe(0)
    expect(root.childElementCount).toBe(1)
  })

  it('fails when the view has no scroll container', async () => {
    const { container } = containerWithCopies(0)

    await expect(reportChatListSize({ container })).rejects.toThrow('The view has no scroll container')
  })
})
