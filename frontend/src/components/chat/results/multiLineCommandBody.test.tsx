import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { CommandInputSummary } from './multiLineCommandBody'

/**
 * Stub the two layout facts this component reads.
 *
 * jsdom lays nothing out, so every element reports a height of zero and the collapsed
 * summary can never clip. The component compares `scrollHeight` against
 * `clientHeight`, and those two getters are the whole of what it measures.
 */
function stubHeights(scrollHeight: number, clientHeight: number): () => void {
  const previous = ['scrollHeight', 'clientHeight'].map(name =>
    [name, Object.getOwnPropertyDescriptor(HTMLElement.prototype, name)] as const)
  Object.defineProperty(HTMLElement.prototype, 'scrollHeight', { configurable: true, get: () => scrollHeight })
  Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get: () => clientHeight })
  return () => {
    for (const [name, descriptor] of previous) {
      if (descriptor)
        Object.defineProperty(HTMLElement.prototype, name, descriptor)
      else
        Reflect.deleteProperty(HTMLElement.prototype, name)
    }
  }
}

/** Mount one summary and collect every overflow answer it reports. */
function mountSummary(heights: { scroll: number, client: number }, startCollapsed = true) {
  const restore = stubHeights(heights.scroll, heights.client)
  const reported: boolean[] = []
  const [collapsed, setCollapsed] = createSignal(startCollapsed)
  render(() => (
    <CommandInputSummary
      command={'one\ntwo\nthree\nfour'}
      collapsed={collapsed()}
      onOverflowChange={value => reported.push(value)}
    />
  ))
  return { reported, setCollapsed, restore }
}

describe('CommandInputSummary', () => {
  it('reports a collapsed summary that clips', () => {
    const { reported, restore } = mountSummary({ scroll: 120, client: 40 })
    expect(reported).toEqual([true])
    restore()
  })

  it('reports nothing for a collapsed summary that fits', () => {
    const { reported, restore } = mountSummary({ scroll: 40, client: 40 })
    expect(reported).toEqual([])
    restore()
  })

  /**
   * Expanding the row must not destroy the fact that made it expandable.
   *
   * `ToolMessage` keeps this answer in `summaryOverflows` and reads it back as
   * `expandable`. Reporting `false` on expand turned the chevron off the moment a
   * reader used it, while `expanded` stayed true in the host's own state -- so the row
   * sat at full height with no control that could collapse it again.
   */
  it('keeps the last collapsed answer when the row expands', () => {
    const { reported, setCollapsed, restore } = mountSummary({ scroll: 120, client: 40 })
    expect(reported).toEqual([true])
    setCollapsed(false)
    expect(reported).toEqual([true])
    restore()
  })

  it('reports nothing for a summary that mounts expanded', () => {
    const { reported, restore } = mountSummary({ scroll: 120, client: 40 }, false)
    expect(reported).toEqual([])
    restore()
  })
})
