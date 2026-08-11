/// <reference types="vitest/globals" />
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { FilterTabBar, nextFilterTab } from './FilterTabBar'

/**
 * A filter bar is a TAB SET: picking a tab swaps the region below it. role=tab
 * brings a keyboard contract with it -- Tab reaches the set, the arrows move
 * within it -- which is what this pins.
 */
describe('nextFilterTab', () => {
  const keys = ['all', 'changed', 'staged', 'unstaged'] as const

  it('moves forward and backward', () => {
    expect(nextFilterTab(keys, 'changed', 'ArrowRight')).toBe('staged')
    expect(nextFilterTab(keys, 'changed', 'ArrowLeft')).toBe('all')
    expect(nextFilterTab(keys, 'changed', 'ArrowDown')).toBe('staged')
    expect(nextFilterTab(keys, 'changed', 'ArrowUp')).toBe('all')
  })

  it('wraps at both ends', () => {
    expect(nextFilterTab(keys, 'unstaged', 'ArrowRight')).toBe('all')
    expect(nextFilterTab(keys, 'all', 'ArrowLeft')).toBe('unstaged')
  })

  it('jumps to the ends with Home and End', () => {
    expect(nextFilterTab(keys, 'staged', 'Home')).toBe('all')
    expect(nextFilterTab(keys, 'staged', 'End')).toBe('unstaged')
  })

  it('ignores keys the tab bar does not own', () => {
    expect(nextFilterTab(keys, 'staged', 'a')).toBeUndefined()
    expect(nextFilterTab(keys, 'staged', 'Enter')).toBeUndefined()
  })

  // The modulo arithmetic divides by the key count, so an empty set would
  // return NaN-indexed undefined for a key the bar DOES own -- a caller cannot
  // tell that apart from "not my key" and would preventDefault on it.
  it('owns no key when the set is empty', () => {
    expect(nextFilterTab([] as const, 'all', 'ArrowRight')).toBeUndefined()
    expect(nextFilterTab([] as const, 'all', 'Home')).toBeUndefined()
  })

  // A current value outside the set (a filter restored from storage after the
  // tabs changed) starts from the first tab rather than walking from -1.
  it('starts from the first tab when the current key is absent', () => {
    expect(nextFilterTab(keys, 'gone' as 'all', 'ArrowRight')).toBe('changed')
  })

  // A one-tab set wraps onto itself in both directions rather than off the end.
  it('stays on the only tab of a single-tab set', () => {
    const one = ['all'] as const
    expect(nextFilterTab(one, 'all', 'ArrowRight')).toBe('all')
    expect(nextFilterTab(one, 'all', 'ArrowLeft')).toBe('all')
    expect(nextFilterTab(one, 'all', 'End')).toBe('all')
  })
})

describe('filterTabBar', () => {
  const tabs = [
    { key: 'all', label: 'All' },
    { key: 'subagent', label: 'Subagents' },
    { key: 'shell', label: 'Shell' },
  ] as const

  function renderBar(onSelect: (key: 'all' | 'subagent' | 'shell') => void = () => {}) {
    const [active, setActive] = createSignal<'all' | 'subagent' | 'shell'>('all')
    const result = render(() => (
      <FilterTabBar
        tabs={tabs}
        active={active()}
        onSelect={(key) => {
          setActive(key)
          onSelect(key)
        }}
        ariaLabel="Filter tasks"
        panelId="panel-1"
        testId="task-filter-tab-bar"
        tabTestId={key => `task-filter-${key}`}
      />
    ))
    return { ...result, active }
  }

  it('names the set and ties each tab to the region it swaps', () => {
    const { getByTestId } = renderBar()
    expect(getByTestId('task-filter-tab-bar')).toHaveAttribute('aria-label', 'Filter tasks')
    expect(getByTestId('task-filter-shell')).toHaveAttribute('aria-controls', 'panel-1')
  })

  it('marks exactly the active tab selected', () => {
    const { getByTestId, active } = renderBar()
    expect(getByTestId('task-filter-all')).toHaveAttribute('aria-selected', 'true')
    expect(getByTestId('task-filter-shell')).toHaveAttribute('aria-selected', 'false')

    fireEvent.click(getByTestId('task-filter-shell'))
    expect(active()).toBe('shell')
    expect(getByTestId('task-filter-all')).toHaveAttribute('aria-selected', 'false')
    expect(getByTestId('task-filter-shell')).toHaveAttribute('aria-selected', 'true')
  })

  // Roving tabindex: Tab reaches the SET, not each tab in it, so only the
  // selected tab stays in the tab order.
  it('keeps only the selected tab in the tab order', () => {
    const { getByTestId } = renderBar()
    expect(getByTestId('task-filter-all')).toHaveAttribute('tabindex', '0')
    expect(getByTestId('task-filter-subagent')).toHaveAttribute('tabindex', '-1')

    fireEvent.click(getByTestId('task-filter-subagent'))
    expect(getByTestId('task-filter-all')).toHaveAttribute('tabindex', '-1')
    expect(getByTestId('task-filter-subagent')).toHaveAttribute('tabindex', '0')
  })

  /**
   * A mouse selection has to move focus for the same reason the keyboard one
   * does: the roving tabindex left the old tab, so focus must not stay behind
   * it. WebKit does not focus a button on click, so without this the newly
   * selected tab holds `tabIndex=0` while focus sits on `<body>` -- and the
   * arrow keys, which the TABLIST handles, then reach nothing.
   */
  it('moves focus to the tab a click selects', () => {
    const { getByTestId } = renderBar()
    fireEvent.click(getByTestId('task-filter-shell'))
    expect(document.activeElement).toBe(getByTestId('task-filter-shell'))
  })

  // The ref map only ever inserted, so a caller whose tabs shrink left a
  // detached button behind for `selectTab` to focus -- which lands on `<body>`.
  it('forgets a tab that leaves the set', () => {
    const [tabList, setTabList] = createSignal<readonly { key: 'all' | 'shell', label: string }[]>([
      { key: 'all', label: 'All' },
      { key: 'shell', label: 'Shell' },
    ])
    const [active, setActive] = createSignal<'all' | 'shell'>('all')
    const { getByTestId, queryByTestId } = render(() => (
      <FilterTabBar
        tabs={tabList()}
        active={active()}
        onSelect={setActive}
        ariaLabel="Filter tasks"
        panelId="panel-1"
        tabTestId={key => `task-filter-${key}`}
      />
    ))
    expect(getByTestId('task-filter-shell')).toBeDefined()

    setTabList([{ key: 'all', label: 'All' }])
    expect(queryByTestId('task-filter-shell')).toBeNull()

    // The surviving tab still takes focus, which a stale entry would steal.
    fireEvent.click(getByTestId('task-filter-all'))
    expect(document.activeElement).toBe(getByTestId('task-filter-all'))
  })

  it('moves the selection with the arrow keys, and focus with it', () => {
    const { getByTestId } = renderBar()
    fireEvent.keyDown(getByTestId('task-filter-tab-bar'), { key: 'ArrowRight' })
    expect(getByTestId('task-filter-subagent')).toHaveAttribute('aria-selected', 'true')
    // The tab that just left the tab order must hand focus over, or the next
    // arrow press lands on nothing.
    expect(document.activeElement).toBe(getByTestId('task-filter-subagent'))
  })

  it('leaves a key it does not own to the browser', () => {
    const { getByTestId } = renderBar()
    const bar = getByTestId('task-filter-tab-bar')
    const event = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    bar.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(false)
    expect(getByTestId('task-filter-all')).toHaveAttribute('aria-selected', 'true')
  })

  // A <button> defaults to type="submit". A filter bar inside a form would
  // otherwise submit it on every tab click.
  it('declares the button type so a tab click cannot submit a form', () => {
    const { getByTestId } = renderBar()
    expect(getByTestId('task-filter-all')).toHaveAttribute('type', 'button')
  })

  it('reports the selected key to the caller', () => {
    const onSelect = vi.fn()
    const { getByTestId } = renderBar(onSelect)
    fireEvent.click(getByTestId('task-filter-shell'))
    expect(onSelect).toHaveBeenCalledWith('shell')
  })
})
