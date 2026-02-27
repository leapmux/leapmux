import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'

// Mock HTMLDialogElement.showModal since jsdom doesn't support it
beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn()
  HTMLDialogElement.prototype.close = vi.fn()
})

describe('confirmDialog', () => {
  it('renders with title and message', () => {
    render(() => (
      <ConfirmDialog
        title="Delete Item"
        onConfirm={() => {}}
        onCancel={() => {}}
      >
        <p>Are you sure?</p>
      </ConfirmDialog>
    ))
    expect(screen.getByText('Delete Item')).toBeDefined()
    expect(screen.getByText('Are you sure?')).toBeDefined()
  })

  it('shows default button labels', () => {
    render(() => (
      <ConfirmDialog
        title="Test"
        onConfirm={() => {}}
        onCancel={() => {}}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    expect(screen.getByText('Cancel')).toBeDefined()
    expect(screen.getByText('OK')).toBeDefined()
  })

  it('shows custom button labels', () => {
    render(() => (
      <ConfirmDialog
        title="Test"
        confirmLabel="Delete"
        cancelLabel="Keep"
        onConfirm={() => {}}
        onCancel={() => {}}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    expect(screen.getByText('Keep')).toBeDefined()
    expect(screen.getByText('Delete')).toBeDefined()
  })

  it('calls onConfirm when confirm button is clicked', () => {
    const onConfirm = vi.fn()
    render(() => (
      <ConfirmDialog
        title="Test"
        onConfirm={onConfirm}
        onCancel={() => {}}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    fireEvent.click(screen.getByText('OK'))
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  it('calls onCancel when cancel button is clicked', () => {
    const onCancel = vi.fn()
    render(() => (
      <ConfirmDialog
        title="Test"
        onConfirm={() => {}}
        onCancel={onCancel}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    fireEvent.click(screen.getByText('Cancel'))
    expect(onCancel).toHaveBeenCalledOnce()
  })

  it('uses ConfirmButton in danger mode', () => {
    render(() => (
      <ConfirmDialog
        title="Test"
        confirmLabel="Delete"
        danger
        onConfirm={() => {}}
        onCancel={() => {}}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    // In danger mode, the confirm button is a ConfirmButton (requires two clicks)
    const deleteBtn = screen.getByText('Delete')
    expect(deleteBtn).toBeDefined()
    // First click arms it, doesn't trigger confirm
    fireEvent.click(deleteBtn)
    expect(screen.getByText('Confirm?')).toBeDefined()
  })

  it('calls showModal on mount', () => {
    render(() => (
      <ConfirmDialog
        title="Test"
        onConfirm={() => {}}
        onCancel={() => {}}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    expect(HTMLDialogElement.prototype.showModal).toHaveBeenCalled()
  })
})
