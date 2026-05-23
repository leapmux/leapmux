import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { BranchContextMenu } from './BranchContextMenu'

// Children contract: BranchContextMenu wraps a DropdownMenu with the two
// action items and inlines the standard three-dot trigger. DropdownMenu
// owns the popover open/close lifecycle; each item click closes the menu
// via DropdownMenu's own onClick → hidePopover wiring.

// Stub Popover API for DropdownMenu.
beforeAll(() => {
  HTMLElement.prototype.showPopover = vi.fn()
  HTMLElement.prototype.hidePopover = vi.fn()
  HTMLElement.prototype.togglePopover = vi.fn()
})

function renderMenu() {
  const onChange = vi.fn()
  const onDelete = vi.fn()
  const result = render(() => (
    <BranchContextMenu onChangeBranch={onChange} onDeleteBranch={onDelete} />
  ))
  // Before the menu opens, the only button rendered is the trigger.
  const trigger = screen.getByRole('button')
  return { onChange, onDelete, trigger, ...result }
}

describe('branchContextMenu', () => {
  it('fires onChangeBranch when Change branch... is clicked', async () => {
    const { onChange, onDelete, trigger } = renderMenu()
    await fireEvent.click(trigger)
    await fireEvent.click(screen.getByText('Change branch...'))
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onDelete).not.toHaveBeenCalled()
  })

  it('fires onDeleteBranch when Delete branch... is clicked', async () => {
    const { onChange, onDelete, trigger } = renderMenu()
    await fireEvent.click(trigger)
    await fireEvent.click(screen.getByText('Delete branch...'))
    expect(onDelete).toHaveBeenCalledTimes(1)
    expect(onChange).not.toHaveBeenCalled()
  })
})
