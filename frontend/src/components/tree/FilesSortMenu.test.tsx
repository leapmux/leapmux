import type { FileSortOrder } from '~/lib/fileSort'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { FilesSortMenu } from './FilesSortMenu'

// Stub the Popover API for DropdownMenu.
beforeAll(() => {
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
  HTMLElement.prototype.togglePopover = vi.fn()
})

function renderMenu(initial: FileSortOrder = { key: 'name', direction: 'asc' }) {
  const [sortOrder, setSortOrder] = createSignal(initial)
  const onChange = vi.fn((order: FileSortOrder) => setSortOrder(order))
  render(() => <FilesSortMenu sortOrder={sortOrder} onChange={onChange} />)
  return { onChange, sortOrder }
}

function checked(testId: string): boolean {
  return screen.getByTestId(testId).getAttribute('aria-checked') === 'true'
}

describe('filesSortMenu', () => {
  it('checks the radio matching the current order', () => {
    renderMenu({ key: 'size', direction: 'desc' })
    expect(checked('files-sort-key-size')).toBe(true)
    expect(checked('files-sort-key-name')).toBe(false)
    expect(checked('files-sort-direction-desc')).toBe(true)
    expect(checked('files-sort-direction-asc')).toBe(false)
  })

  it('offers every criterion and both directions', () => {
    renderMenu()
    for (const key of ['name', 'modified', 'size', 'type'])
      expect(screen.getByTestId(`files-sort-key-${key}`)).toBeInTheDocument()
    for (const direction of ['asc', 'desc'])
      expect(screen.getByTestId(`files-sort-direction-${direction}`)).toBeInTheDocument()
  })

  it('reports a criterion change while keeping the direction', () => {
    const { onChange } = renderMenu({ key: 'name', direction: 'desc' })
    fireEvent.click(screen.getByTestId('files-sort-key-modified'))
    expect(onChange).toHaveBeenCalledWith({ key: 'modified', direction: 'desc' })
  })

  it('reports a direction change while keeping the criterion', () => {
    const { onChange } = renderMenu({ key: 'size', direction: 'asc' })
    fireEvent.click(screen.getByTestId('files-sort-direction-desc'))
    expect(onChange).toHaveBeenCalledWith({ key: 'size', direction: 'desc' })
  })

  /**
   * The popover holds two independent settings, so it must survive the first
   * click: a menu that dismissed on selection would put the direction out of
   * reach without reopening.
   */
  it('lets a criterion and a direction be chosen in one visit', () => {
    const { onChange, sortOrder } = renderMenu({ key: 'name', direction: 'asc' })
    fireEvent.click(screen.getByTestId('files-sort-key-size'))
    fireEvent.click(screen.getByTestId('files-sort-direction-desc'))
    expect(onChange).toHaveBeenNthCalledWith(2, { key: 'size', direction: 'desc' })
    expect(sortOrder()).toEqual({ key: 'size', direction: 'desc' })
  })

  it('adapts the direction labels to the criterion', () => {
    const { unmount } = render(() => (
      <FilesSortMenu sortOrder={() => ({ key: 'name', direction: 'asc' })} onChange={vi.fn()} />
    ))
    expect(screen.getByTestId('files-sort-direction-asc').textContent).toContain('A → Z')
    unmount()

    render(() => (
      <FilesSortMenu sortOrder={() => ({ key: 'size', direction: 'asc' })} onChange={vi.fn()} />
    ))
    expect(screen.getByTestId('files-sort-direction-asc').textContent).toContain('Smallest first')
    expect(screen.getByTestId('files-sort-direction-desc').textContent).toContain('Largest first')
  })

  /**
   * One label on the popover cannot name two groups, and six consecutive
   * radios would otherwise announce as one six-option group.
   */
  it('names each radio group separately', () => {
    renderMenu()
    const groups = [...document.querySelectorAll('[role="group"]')]
    expect(groups.map(g => g.getAttribute('aria-label'))).toEqual(['Sort by', 'Order'])
    // The popover is a plain div, so the menu role has to come from inside it.
    expect(document.querySelector('[role="menu"]')?.getAttribute('aria-label')).toBe('Sort files')
  })

  it('shows the current order in the trigger tooltip and flips its icon', () => {
    const { unmount } = render(() => (
      <FilesSortMenu sortOrder={() => ({ key: 'modified', direction: 'desc' })} onChange={vi.fn()} />
    ))
    expect(screen.getByTestId('files-sort-toggle').getAttribute('aria-label'))
      .toBe('Sort by last modified (Newest first)')
    const descIcon = screen.getByTestId('files-sort-toggle').innerHTML
    unmount()

    render(() => (
      <FilesSortMenu sortOrder={() => ({ key: 'modified', direction: 'asc' })} onChange={vi.fn()} />
    ))
    expect(screen.getByTestId('files-sort-toggle').innerHTML).not.toBe(descIcon)
  })
})
