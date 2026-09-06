import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { DisabledReasonMenuItem } from './DisabledReasonMenuItem'

function renderItem(reason: string | undefined, onClick = vi.fn()) {
  render(() => (
    <menu data-testid="host">
      <DisabledReasonMenuItem reason={reason} onClick={onClick} data-testid="item">
        New agent...
      </DisabledReasonMenuItem>
    </menu>
  ))
  return { onClick, item: screen.getByTestId('item') }
}

describe('disabledReasonMenuItem', () => {
  it('is usable and fires when there is no reason', () => {
    const { onClick, item } = renderItem(undefined)

    expect(item).not.toBeDisabled()
    fireEvent.click(item)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  // ONE prop drives both halves. Two props would let a caller disable an item
  // and say nothing, which is the state a user cannot act on.
  it('is disabled exactly when a reason exists', () => {
    const { onClick, item } = renderItem('This Worker is offline.')

    expect(item).toBeDisabled()
    fireEvent.click(item)
    expect(onClick).not.toHaveBeenCalled()
  })

  // Five of the seven sites this replaced omitted `type`, which defaults to
  // `submit` -- so inside a form the item also submitted it.
  it('is a button, never a submit', () => {
    const { item } = renderItem(undefined)

    expect(item.getAttribute('type')).toBe('button')
  })

  // A `title` long enough to state a reason BECOMES the accessible name, so a
  // screen reader announces the remedy where the label belongs and every
  // `getByRole(..., { name })` lookup stops matching.
  it('keeps its own accessible name, and sets no title', () => {
    const { item } = renderItem('This Worker is offline. Opening a tab needs the machine the repository is on.')

    expect(item.hasAttribute('title')).toBe(false)
    expect(screen.getByRole('menuitem', { name: 'New agent...', hidden: true })).toBe(item)
  })

  // The prop compiles to a getter, so a caller may pass a value that changes.
  // Freezing the disabled state at build time is the bug this guards.
  it('follows a reason that appears after the first render', () => {
    const [reason, setReason] = createSignal<string | undefined>(undefined)
    const onClick = vi.fn()
    render(() => (
      <menu>
        <DisabledReasonMenuItem reason={reason()} onClick={onClick} data-testid="item">
          New agent...
        </DisabledReasonMenuItem>
      </menu>
    ))
    const item = screen.getByTestId('item')
    expect(item).not.toBeDisabled()

    setReason('This Worker is offline.')

    expect(item).toBeDisabled()
  })

  it('carries an extra class for a row that has to look dangerous', () => {
    const onClick = vi.fn()
    render(() => (
      <menu>
        <DisabledReasonMenuItem reason={undefined} onClick={onClick} class="danger" data-testid="item">
          Delete branch...
        </DisabledReasonMenuItem>
      </menu>
    ))

    expect(screen.getByTestId('item')).toHaveClass('danger')
  })
})
