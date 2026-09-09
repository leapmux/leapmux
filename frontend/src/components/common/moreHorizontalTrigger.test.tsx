import type { DropdownTriggerProps } from './DropdownMenu'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import * as iconButtonStyles from './IconButton.css'
import { moreHorizontalTrigger, rowContextMenuTrigger } from './moreHorizontalTrigger'

// Built per test. One shared object would carry its `vi.fn()` call counts from
// the case before, so a forwarding assertion could pass on another test's click.
function triggerProps(overrides: Partial<DropdownTriggerProps> = {}): DropdownTriggerProps {
  return {
    'aria-expanded': false,
    'ref': () => {},
    'onPointerDown': vi.fn(),
    'onClick': vi.fn(),
    ...overrides,
  }
}

describe('moreHorizontalTrigger', () => {
  it('uses a 24px button with a 14px icon', () => {
    render(() => moreHorizontalTrigger({ title: 'More actions' })(triggerProps()))

    const button = screen.getByRole('button', { name: 'More actions' })
    expect(button.classList).toContain(iconButtonStyles.sizeMd)
    expect(button.classList).not.toContain(iconButtonStyles.sizeSm)

    const icon = button.querySelector('svg')
    expect(icon).toHaveAttribute('width', '14')
    expect(icon).toHaveAttribute('height', '14')
  })

  // Forwarding is the helper's whole job. Dropping `onClick` makes every row
  // menu refuse to open, and a size-only suite stays green through that.
  it('forwards the menu handlers and the expanded state', () => {
    let attached: HTMLElement | undefined
    const props = triggerProps({ 'aria-expanded': true, 'ref': el => (attached = el) })
    render(() => moreHorizontalTrigger({ title: 'More actions' })(props))

    const button = screen.getByRole('button', { name: 'More actions' })
    expect(button).toHaveAttribute('aria-expanded', 'true')

    fireEvent.pointerDown(button)
    expect(props.onPointerDown).toHaveBeenCalledOnce()

    fireEvent.click(button)
    expect(props.onClick).toHaveBeenCalledOnce()
    expect(attached).toBe(button)
  })

  // The row owns the click that selects it, so the trigger must claim the event
  // before it reaches the row, or opening a menu also selects the row under it.
  it('keeps the row from seeing the press that opens the menu', () => {
    const rowClick = vi.fn()
    const rowPointerDown = vi.fn()
    render(() => (
      <div onPointerDown={rowPointerDown} onClick={rowClick}>
        {moreHorizontalTrigger({ title: 'More actions' })(triggerProps())}
      </div>
    ))

    const button = screen.getByRole('button', { name: 'More actions' })
    fireEvent.pointerDown(button)
    fireEvent.click(button)

    expect(rowPointerDown).not.toHaveBeenCalled()
    expect(rowClick).not.toHaveBeenCalled()
  })
})

describe('rowContextMenuTrigger', () => {
  it('carries the sidebar row-menu class and the caller test id', () => {
    render(() => rowContextMenuTrigger({ 'data-testid': 'row-menu' })(triggerProps()))

    const button = screen.getByTestId('row-menu')
    expect(button.classList).toContain(iconButtonStyles.sizeMd)
    expect(button.className).toMatch(/menuTrigger/)
  })
})
