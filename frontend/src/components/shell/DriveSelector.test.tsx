import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DriveSelector } from './DriveSelector'

afterEach(() => {
  cleanup()
})

function renderSelector(
  { value, roots, onSelect = vi.fn() }: { value: string, roots: string[], onSelect?: (root: string) => void },
) {
  const view = render(() => (
    <DriveSelector value={value} roots={roots} onSelect={onSelect} />
  ))
  return { ...view, onSelect }
}

function openMenu() {
  fireEvent.click(screen.getByTestId('drive-selector-trigger'))
}

/**
 * The option rows, read by their ROLE ATTRIBUTE rather than through
 * `getAllByRole`.
 *
 * jsdom does not implement the popover API, so the menu stays outside the
 * accessibility tree and every role QUERY skips it. The attribute is what the
 * component is responsible for, and it is what a real browser turns into the
 * role, so asserting on it pins the same contract.
 */
function optionRows(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('[role="menuitemradio"]')]
}

describe('driveSelector', () => {
  it('shows the current root on the trigger', () => {
    renderSelector({ value: 'D:\\', roots: ['C:\\', 'D:\\'] })

    expect(screen.getByTestId('drive-selector-trigger')).toHaveTextContent('D:\\')
  })

  it('checks exactly the current root', () => {
    renderSelector({ value: 'D:\\', roots: ['C:\\', 'D:\\'] })
    openMenu()

    const items = optionRows()
    expect(items).toHaveLength(2)
    expect(items.filter(el => el.getAttribute('aria-checked') === 'true')).toHaveLength(1)
    expect(screen.getByTestId('drive-option-d')).toHaveAttribute('aria-checked', 'true')
  })

  // Windows compares drive letters without regard to case, so a value spelled
  // differently from the reported root is the SAME root, not a missing one.
  it('matches the current root case-insensitively without duplicating it', () => {
    renderSelector({ value: 'c:\\', roots: ['C:\\'] })
    openMenu()

    expect(optionRows()).toHaveLength(1)
    expect(screen.getByTestId('drive-option-c')).toHaveAttribute('aria-checked', 'true')
  })

  // Windows never enumerates a UNC share as a logical drive. Without the
  // current value in the list, every row is unchecked and one stray click
  // leaves the user with no way back to where they were.
  it('leads with a root the worker did not report', () => {
    renderSelector({ value: '\\\\srv\\share\\', roots: ['C:\\', 'D:\\'] })
    openMenu()

    const items = optionRows()
    expect(items).toHaveLength(3)
    expect(items[0]).toHaveTextContent('\\\\srv\\share\\')
    expect(items[0]).toHaveAttribute('aria-checked', 'true')
  })

  it('calls onSelect with the chosen root', () => {
    const { onSelect } = renderSelector({ value: 'C:\\', roots: ['C:\\', 'D:\\'] })
    openMenu()

    fireEvent.click(screen.getByTestId('drive-option-d'))

    expect(onSelect).toHaveBeenCalledWith('D:\\')
  })

  // A native <select> opens the OS picker, which ignores the app's theme and
  // typography. The project bans it outright; this pins that.
  it('uses radio menu items, never a native select', () => {
    const { container } = renderSelector({ value: 'C:\\', roots: ['C:\\', 'D:\\'] })
    openMenu()

    expect(container.querySelector('select')).toBeNull()
    expect(optionRows()).toHaveLength(2)
  })

  // The trigger's own text is a bare drive letter, so without an explicit name
  // a screen reader announces the current drive where the control's PURPOSE
  // belongs.
  it('gives the trigger an accessible name', () => {
    renderSelector({ value: 'C:\\', roots: ['C:\\', 'D:\\'] })

    expect(screen.getByRole('button', { name: 'Drive' })).toBeInTheDocument()
  })

  // A <menu> of radio items carries no name of its own either. jsdom keeps the
  // popover out of the accessibility tree, so the attribute is what to assert.
  it('gives the menu an accessible name', () => {
    renderSelector({ value: 'C:\\', roots: ['C:\\', 'D:\\'] })
    openMenu()

    expect(screen.getByTestId('drive-selector-menu')).toHaveAttribute('aria-label', 'Drive')
  })
})
