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

function renderMenu(disabledReason?: string) {
  const onChange = vi.fn()
  const onDelete = vi.fn()
  const result = render(() => (
    <BranchContextMenu onChangeBranch={onChange} onDeleteBranch={onDelete} disabledReason={disabledReason} />
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

  // Offline gating. Both items stay VISIBLE and disabled rather than
  // disappearing, because a row that silently loses its menu reads as a bug while
  // a dimmed item with a title says which machine to bring back.
  //
  // Native `disabled` is deliberate: the dimming and not-allowed cursor come from
  // oat's global `:disabled` rule, which every other menu in the app relies on.
  // The cost is that a disabled button leaves the focus order, so the title is
  // mouse-only -- see the note on disabledReason.
  describe('when the worker is offline', () => {
    const reason = 'Worker "mac-mini" is offline'

    it('disables both items and describes them with the reason', async () => {
      const { trigger } = renderMenu(reason)
      await fireEvent.click(trigger)
      for (const label of ['Change branch...', 'Delete branch...']) {
        const item = screen.getByText(label)
        expect(item).toBeDisabled()
        // Through the Tooltip's offscreen description, not `title`. A reason
        // this long on `title` BECAME the item's accessible name, so a screen
        // reader announced it in place of "Change branch...".
        expect(item).not.toHaveAttribute('title')
        const describedBy = item.getAttribute('aria-describedby')
        expect(describedBy).toBeTruthy()
        expect(document.getElementById(describedBy!)?.textContent).toBe(reason)
      }
    })

    it('does not fire either action while disabled', async () => {
      const { onChange, onDelete, trigger } = renderMenu(reason)
      await fireEvent.click(trigger)
      await fireEvent.click(screen.getByText('Change branch...'))
      await fireEvent.click(screen.getByText('Delete branch...'))
      expect(onChange).not.toHaveBeenCalled()
      expect(onDelete).not.toHaveBeenCalled()
    })

    it('leaves both items enabled when no reason is given', async () => {
      const { trigger } = renderMenu()
      await fireEvent.click(trigger)
      for (const label of ['Change branch...', 'Delete branch...']) {
        expect(screen.getByText(label)).toBeEnabled()
      }
    })
  })

  describe('with a custom trigger', () => {
    it('renders the custom trigger instead of the kebab', () => {
      const onChange = vi.fn()
      const onDelete = vi.fn()
      render(() => (
        <BranchContextMenu
          onChangeBranch={onChange}
          onDeleteBranch={onDelete}
          trigger={triggerProps => (
            <button data-testid="custom-trigger" {...triggerProps}>
              main
            </button>
          )}
        />
      ))

      // The custom trigger renders, not the kebab.
      expect(screen.getByTestId('custom-trigger')).toBeInTheDocument()
      expect(screen.getByText('main')).toBeInTheDocument()
    })

    it('fires onChangeBranch via the custom trigger opening the menu', async () => {
      const onChange = vi.fn()
      const onDelete = vi.fn()
      render(() => (
        <BranchContextMenu
          onChangeBranch={onChange}
          onDeleteBranch={onDelete}
          trigger={triggerProps => (
            <button data-testid="custom-trigger" {...triggerProps}>
              main
            </button>
          )}
        />
      ))

      await fireEvent.click(screen.getByTestId('custom-trigger'))
      await fireEvent.click(screen.getByText('Change branch...'))
      expect(onChange).toHaveBeenCalledTimes(1)
    })
  })
})
