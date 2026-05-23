/// <reference types="vitest/globals" />
import { render, waitFor } from '@solidjs/testing-library'
import { createSignal, Show } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { Dialog, DialogColumns } from './Dialog'
import * as styles from './Dialog.css'

// jsdom does not implement the native <dialog> API.
// Stub showModal so the component can mount without errors.
let originalShowModal: typeof HTMLDialogElement.prototype.showModal | undefined
let originalClose: typeof HTMLDialogElement.prototype.close

beforeAll(() => {
  if (!HTMLDialogElement.prototype.showModal) {
    originalShowModal = undefined
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.setAttribute('open', '')
    })
  }
  else {
    originalShowModal = HTMLDialogElement.prototype.showModal
  }

  originalClose = HTMLDialogElement.prototype.close
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  }
})

afterAll(() => {
  if (originalShowModal) {
    HTMLDialogElement.prototype.showModal = originalShowModal
  }
  HTMLDialogElement.prototype.close = originalClose
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

  it('closes on Escape when not busy', async () => {
    const onClose = vi.fn()
    const { container } = render(() => (
      <Dialog title="Test" onClose={onClose}>
        <p>Content</p>
      </Dialog>
    ))

    const dialog = container.querySelector('dialog')!
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    // User-initiated close paths run an exit animation before calling
    // `onClose` (see `Dialog.tsx`'s `beginClose`); poll until it fires.
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it('closes on backdrop click when not busy', async () => {
    const onClose = vi.fn()
    const { container } = render(() => (
      <Dialog title="Test" onClose={onClose}>
        <p>Content</p>
      </Dialog>
    ))

    const dialog = container.querySelector('dialog')!
    // Mock bounding rect so the click coordinates fall outside.
    dialog.getBoundingClientRect = () => ({ top: 100, left: 100, right: 500, bottom: 500, width: 400, height: 400, x: 100, y: 100, toJSON: () => {} })
    // Simulate a full press+release on the backdrop (target is dialog, coordinates outside content).
    dialog.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientX: 10, clientY: 10 }))
    dialog.dispatchEvent(new MouseEvent('click', { bubbles: true, clientX: 10, clientY: 10 }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it('does not close on backdrop click when busy', () => {
    const onClose = vi.fn()
    const { container } = render(() => (
      <Dialog title="Test" busy onClose={onClose}>
        <p>Content</p>
      </Dialog>
    ))

    const dialog = container.querySelector('dialog')!
    dialog.getBoundingClientRect = () => ({ top: 100, left: 100, right: 500, bottom: 500, width: 400, height: 400, x: 100, y: 100, toJSON: () => {} })
    dialog.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientX: 10, clientY: 10 }))
    dialog.dispatchEvent(new MouseEvent('click', { bubbles: true, clientX: 10, clientY: 10 }))
    expect(onClose).not.toHaveBeenCalled()
  })

  it('does not close when a drag started inside the dialog ends on the backdrop', () => {
    const onClose = vi.fn()
    const { container } = render(() => (
      <Dialog title="Test" onClose={onClose}>
        <p>Content</p>
      </Dialog>
    ))

    const dialog = container.querySelector('dialog')!
    dialog.getBoundingClientRect = () => ({ top: 100, left: 100, right: 500, bottom: 500, width: 400, height: 400, x: 100, y: 100, toJSON: () => {} })
    const content = container.querySelector('p')!
    // Press starts inside the dialog content (e.g. beginning of a text selection).
    content.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientX: 300, clientY: 300 }))
    // Release ends on the backdrop — target is the dialog itself, coords outside content.
    dialog.dispatchEvent(new MouseEvent('click', { bubbles: true, clientX: 10, clientY: 10 }))
    expect(onClose).not.toHaveBeenCalled()
  })

  it('does not close when a drag started on the backdrop ends inside the dialog', () => {
    const onClose = vi.fn()
    const { container } = render(() => (
      <Dialog title="Test" onClose={onClose}>
        <p>Content</p>
      </Dialog>
    ))

    const dialog = container.querySelector('dialog')!
    dialog.getBoundingClientRect = () => ({ top: 100, left: 100, right: 500, bottom: 500, width: 400, height: 400, x: 100, y: 100, toJSON: () => {} })
    // Press starts on the backdrop.
    dialog.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientX: 10, clientY: 10 }))
    // Release ends on the dialog content — click target is the content element.
    const content = container.querySelector('p')!
    content.dispatchEvent(new MouseEvent('click', { bubbles: true, clientX: 300, clientY: 300 }))
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

  it('absorbs initial focus on the body container instead of the close button', () => {
    const onClose = vi.fn()

    const { container } = render(() => (
      <Dialog title="Test" onClose={onClose}>
        <form>
          <select data-testid="worker-select">
            <option>Worker 1</option>
          </select>
          <button type="submit">Create</button>
        </form>
      </Dialog>
    ))

    // The body <div> (tabindex=-1) owns initial focus so that neither the
    // close button nor any form control gains focus on open.
    const body = container.querySelector('dialog > div[tabindex="-1"]')!
    expect(document.activeElement).toBe(body)
  })

  it('triggers submit button on Enter key', () => {
    const onClose = vi.fn()
    const onSubmit = vi.fn((e: Event) => e.preventDefault())

    const { container } = render(() => (
      <Dialog title="Test" onClose={onClose}>
        <form onSubmit={onSubmit}>
          <input type="text" data-testid="text-input" />
          <button type="submit">Create</button>
        </form>
      </Dialog>
    ))

    const input = container.querySelector('input')!
    input.focus()
    const dialog = container.querySelector('dialog')!
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onSubmit).toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('does not trigger submit on Enter when submit button is disabled', () => {
    const onClose = vi.fn()
    const onSubmit = vi.fn((e: Event) => e.preventDefault())

    const { container } = render(() => (
      <Dialog title="Test" onClose={onClose}>
        <form onSubmit={onSubmit}>
          <input type="text" />
          <button type="submit" disabled>Create</button>
        </form>
      </Dialog>
    ))

    const input = container.querySelector('input')!
    input.focus()
    const dialog = container.querySelector('dialog')!
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('does not trigger submit on Enter when focused on a button', () => {
    const onClose = vi.fn()
    const onSubmit = vi.fn((e: Event) => e.preventDefault())

    const { container } = render(() => (
      <Dialog title="Test" onClose={onClose}>
        <form onSubmit={onSubmit}>
          <button type="button">Cancel</button>
          <button type="submit">Create</button>
        </form>
      </Dialog>
    ))

    const cancelBtn = container.querySelector('button[type="button"]') as HTMLButtonElement
    cancelBtn.focus()
    const dialog = container.querySelector('dialog')!
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('does not close on Escape when busy', () => {
    const onClose = vi.fn()
    const { container } = render(() => (
      <Dialog title="Test" busy onClose={onClose}>
        <p>Content</p>
      </Dialog>
    ))

    const dialog = container.querySelector('dialog')!
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(dialog.hasAttribute('open')).toBe(true)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('does not trigger submit on Enter when focused on a textarea', () => {
    const onClose = vi.fn()
    const onSubmit = vi.fn((e: Event) => e.preventDefault())

    const { container } = render(() => (
      <Dialog title="Test" onClose={onClose}>
        <form onSubmit={onSubmit}>
          <textarea />
          <button type="submit">Create</button>
        </form>
      </Dialog>
    ))

    const textarea = container.querySelector('textarea')!
    textarea.focus()
    const dialog = container.querySelector('dialog')!
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('does not trigger submit on Enter when busy', () => {
    const onClose = vi.fn()
    const onSubmit = vi.fn((e: Event) => e.preventDefault())

    const { container } = render(() => (
      <Dialog title="Test" busy onClose={onClose}>
        <form onSubmit={onSubmit}>
          <input type="text" />
          <button type="submit">Create</button>
        </form>
      </Dialog>
    ))

    const input = container.querySelector('input')!
    input.focus()
    const dialog = container.querySelector('dialog')!
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onSubmit).not.toHaveBeenCalled()
  })

  // ----- DialogColumns layout ---------------------------------------------

  it('dialogColumns renders both panels in two-column mode', () => {
    const { container } = render(() => (
      <DialogColumns
        left={<span data-testid="left">L</span>}
        right={<span data-testid="right">R</span>}
      />
    ))
    expect(container.querySelector(`.${styles.twoColumn}`)).not.toBeNull()
    expect(container.querySelector(`.${styles.leftPanel}`)).not.toBeNull()
    expect(container.querySelector(`.${styles.rightPanel}`)).not.toBeNull()
    expect(container.querySelector('[data-testid="left"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="right"]')).not.toBeNull()
  })

  it('dialogColumns skips the right panel in single-column mode (no empty <div> in DOM)', () => {
    const { container } = render(() => (
      <DialogColumns
        twoColumn={false}
        left={<span data-testid="left">L</span>}
        right={<span data-testid="right">R</span>}
      />
    ))
    // Single-column mode wraps left only; the rightPanel wrapper must not
    // be in the DOM at all (regression for the original
    // `<div class={twoColumn ? rightPanel : undefined}>` that emitted an
    // empty wrapper even when its content was hidden).
    expect(container.querySelector(`.${styles.singleColumn}`)).not.toBeNull()
    expect(container.querySelector(`.${styles.twoColumn}`)).toBeNull()
    expect(container.querySelector(`.${styles.rightPanel}`)).toBeNull()
    expect(container.querySelector('[data-testid="right"]')).toBeNull()
    expect(container.querySelector('[data-testid="left"]')).not.toBeNull()
  })

  it('dialogColumns skips the right panel when right is undefined even in two-column mode', () => {
    const { container } = render(() => (
      <DialogColumns
        left={<span data-testid="left">L</span>}
      />
    ))
    // twoColumn defaults to true, but with no right child the rightPanel
    // wrapper still must not render — keeps the DOM clean for callers
    // that conditionally omit the right slot.
    expect(container.querySelector(`.${styles.twoColumn}`)).not.toBeNull()
    expect(container.querySelector(`.${styles.rightPanel}`)).toBeNull()
    expect(container.querySelector('[data-testid="left"]')).not.toBeNull()
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
