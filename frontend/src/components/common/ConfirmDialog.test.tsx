/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ConfirmDialog } from './ConfirmDialog'

// jsdom does not implement the native <dialog> API.
beforeAll(() => {
  if (!HTMLDialogElement.prototype.showModal) {
    HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
      this.setAttribute('open', '')
    })
  }

  const _originalClose = HTMLDialogElement.prototype.close
  HTMLDialogElement.prototype.close = function (..._args: Parameters<typeof _originalClose>) {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  }
})

describe('confirm dialog', () => {
  it('disables cancel and confirm buttons when busy', () => {
    const onConfirm = vi.fn()
    const onCancel = vi.fn()

    const { getByRole } = render(() => (
      <ConfirmDialog title="Test" busy onConfirm={onConfirm} onCancel={onCancel}>
        <p>Are you sure?</p>
      </ConfirmDialog>
    ))

    const cancelButton = getByRole('button', { name: 'Cancel' })
    const okButton = getByRole('button', { name: 'OK' })

    expect(cancelButton).toBeDisabled()
    expect(okButton).toBeDisabled()
  })

  it('enables cancel and confirm buttons when not busy', () => {
    const { getByRole } = render(() => (
      <ConfirmDialog title="Test" onConfirm={() => {}} onCancel={() => {}}>
        <p>Are you sure?</p>
      </ConfirmDialog>
    ))

    const cancelButton = getByRole('button', { name: 'Cancel' })
    const okButton = getByRole('button', { name: 'OK' })

    expect(cancelButton).not.toBeDisabled()
    expect(okButton).not.toBeDisabled()
  })
})
