import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { DragHandle } from '~/components/common/DragHandle'
import { flush } from '~/test-support/async'
import { pointerEvent } from '~/test-support/pointer'

describe('dragHandle', () => {
  it('renders the grip as a pointer-only affordance', () => {
    const { getByTestId } = render(() => (
      <DragHandle visibility="always" activators={() => undefined} testId="grip" />
    ))

    const grip = getByTestId('grip')
    expect(grip).toHaveAttribute('data-drag-handle', '')
    expect(grip).toHaveAttribute('aria-hidden', 'true')
  })

  it('hands a press on the grip to the raw activators, both inputs', async () => {
    const handler = vi.fn()
    const { getByTestId } = render(() => (
      <DragHandle visibility="auto" activators={() => ({ onPointerdown: handler })} testId="grip" />
    ))
    // Solid flushes the binding effect on a microtask.
    await flush()

    fireEvent(getByTestId('grip'), pointerEvent('pointerdown', { pointerType: 'touch' }))
    fireEvent(getByTestId('grip'), pointerEvent('pointerdown', { pointerType: 'mouse' }))

    expect(handler).toHaveBeenCalledTimes(2)
  })
})
