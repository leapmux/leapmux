import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { KeyPinMismatchDialog } from './KeyPinMismatchDialog'

// jsdom does not implement the native dialog methods. These spies keep the open
// attribute and the close event consistent, so mounting and dismissing match a
// browser.
beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
    this.setAttribute('open', '')
  })
  HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  })
})

describe('KeyPinMismatchDialog', () => {
  const renderDialog = (resolve: (decision: 'accept' | 'reject') => void) =>
    render(() => (
      <KeyPinMismatchDialog
        workerId="w-1"
        expectedFingerprint="deep-idea-obey-tack"
        actualFingerprint="bold-cave-iron-moss"
        resolve={resolve}
      />
    ))

  it('states that a worker keeps its keys while it stays registered', () => {
    renderDialog(() => {})
    expect(screen.getByTestId('key-pin-mismatch-dialog')).toHaveTextContent(
      'differs from the key pinned for it at an earlier connection',
    )
    expect(screen.getByTestId('key-pin-mismatch-dialog')).toHaveTextContent(
      'A worker keeps its keys for as long as it stays registered',
    )
  })

  it('tells the user to accept only against the fingerprint the worker logs', () => {
    renderDialog(() => {})
    expect(screen.getByTestId('key-pin-mismatch-dialog')).toHaveTextContent(
      'Accept only after you confirm that the Actual fingerprint matches the one the worker logs when it starts',
    )
  })

  it('shows both fingerprints', () => {
    renderDialog(() => {})
    expect(screen.getByTestId('expected-fingerprint')).toHaveTextContent('deep-idea-obey-tack')
    expect(screen.getByTestId('actual-fingerprint')).toHaveTextContent('bold-cave-iron-moss')
  })

  it('resolves reject on a click', () => {
    const resolve = vi.fn()
    renderDialog(resolve)

    fireEvent.click(screen.getByTestId('key-pin-reject'))
    expect(resolve).toHaveBeenCalledWith('reject')
  })

  it('resolves reject when the dialog is dismissed', () => {
    const resolve = vi.fn()
    renderDialog(resolve)

    fireEvent(screen.getByTestId('key-pin-mismatch-dialog'), new Event('close'))
    expect(resolve).toHaveBeenCalledWith('reject')
  })

  it('resolves accept only on the confirmation click', () => {
    const resolve = vi.fn()
    renderDialog(resolve)

    fireEvent.click(screen.getByTestId('key-pin-accept'))
    expect(resolve).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTestId('key-pin-accept'))
    expect(resolve).toHaveBeenCalledWith('accept')
  })
})
