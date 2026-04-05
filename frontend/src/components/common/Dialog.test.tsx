/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'
import { createSignal, Show } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { Dialog } from './Dialog'

// jsdom does not implement the native <dialog> API.
// Stub showModal so the component can mount without errors.
beforeAll(() => {
  if (!HTMLDialogElement.prototype.showModal) {
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.setAttribute('open', '')
    })
  }

  // Ensure close() fires the 'close' event like real browsers do.
  const _originalClose = HTMLDialogElement.prototype.close
  HTMLDialogElement.prototype.close = function (..._args: Parameters<typeof _originalClose>) {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  }
})

describe('dialog', () => {
  it('does not fire onClose when unmounted by parent', () => {
    const onClose = vi.fn()
    const [show, setShow] = createSignal(true)

    render(() => (
      <Show when={show()}>
        <Dialog title="Test" onClose={onClose}>
          <p>Content</p>
        </Dialog>
      </Show>
    ))

    // Unmount the dialog by toggling the signal
    setShow(false)

    // onClose should NOT have been called during cleanup
    expect(onClose).not.toHaveBeenCalled()
  })

  it('disables the X button when busy', () => {
    const onClose = vi.fn()

    const { getByRole } = render(() => (
      <Dialog title="Test" busy onClose={onClose}>
        <p>Content</p>
      </Dialog>
    ))

    const closeButton = getByRole('button', { name: 'Close' })
    expect(closeButton).toBeDisabled()
    closeButton.click()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('closes on backdrop click when not busy', () => {
    const onClose = vi.fn()
    const { container } = render(() => (
      <Dialog title="Test" onClose={onClose}>
        <p>Content</p>
      </Dialog>
    ))

    const dialog = container.querySelector('dialog')!
    // Mock bounding rect so the click coordinates fall outside.
    dialog.getBoundingClientRect = () => ({ top: 100, left: 100, right: 500, bottom: 500, width: 400, height: 400, x: 100, y: 100, toJSON: () => {} })
    // Simulate a click on the backdrop (target is dialog, coordinates outside content).
    dialog.dispatchEvent(new MouseEvent('click', { bubbles: true, clientX: 10, clientY: 10 }))
    expect(onClose).toHaveBeenCalled()
  })

  it('does not close on backdrop click when busy', () => {
    const onClose = vi.fn()
    const { container } = render(() => (
      <Dialog title="Test" busy onClose={onClose}>
        <p>Content</p>
      </Dialog>
    ))

    const dialog = container.querySelector('dialog')!
    dialog.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(onClose).not.toHaveBeenCalled()
  })

  it('does not call onClose when native close event fires while busy', () => {
    const onClose = vi.fn()

    const { container } = render(() => (
      <Dialog title="Test" busy onClose={onClose}>
        <p>Content</p>
      </Dialog>
    ))

    const dialog = container.querySelector('dialog')!
    dialog.dispatchEvent(new Event('close'))
    expect(onClose).not.toHaveBeenCalled()
  })

  it('does not access stale keyed Show accessor on cleanup', () => {
    const [state, setState] = createSignal<{ resolve: (v: boolean) => void } | null>({
      resolve: vi.fn(),
    })

    render(() => (
      <Show when={state()}>
        {accessor => (
          <Dialog
            title="Confirm"
            onClose={() => {
              // This would throw "stale value from <Show>" if the Dialog
              // fires onClose during its own cleanup after the Show unmounts.
              accessor().resolve(false)
              setState(null)
            }}
          >
            <p>Are you sure?</p>
          </Dialog>
        )}
      </Show>
    ))

    // Simulate the confirm flow: set state to null, which unmounts the Show.
    // Dialog.onCleanup calls dialogRef.close(), which fires the 'close' event.
    // Without the fix, this would call onClose -> accessor() -> throw.
    expect(() => setState(null)).not.toThrow()
  })
})
