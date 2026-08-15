import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { DragHandle } from '~/components/common/DragHandle'

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
    await Promise.resolve()
    await Promise.resolve()

    fireEvent.pointerDown(getByTestId('grip'))

    expect(handler).toHaveBeenCalledOnce()
  })

  it('renders without activators without throwing', () => {
    const { getByTestId } = render(() => (
      <DragHandle visibility="always" activators={() => undefined} testId="grip" />
    ))

    expect(getByTestId('grip')).toBeInTheDocument()
  })
})
