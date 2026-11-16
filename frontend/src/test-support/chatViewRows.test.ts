import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { measureHiddenCopies } from './chatViewRows'
import { installControllableResizeObserver } from './resizeObserverStub'

describe('measureHiddenCopies', () => {
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
