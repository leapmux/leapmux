import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ConfirmDialog } from './ConfirmDialog'

// jsdom does not implement the native dialog methods.
// These spies keep the open attribute and close event consistent.
// Component mount and cleanup then match the browser behavior.
beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
    this.setAttribute('open', '')
  })
  HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  })
})

describe('ConfirmDialog', () => {
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
    expect(screen.getByText('Delete Item')).toBeInTheDocument()
    expect(screen.getByText('Are you sure?')).toBeInTheDocument()
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
    expect(screen.getByText('Cancel')).toBeInTheDocument()
    expect(screen.getByText('OK')).toBeInTheDocument()
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
    expect(screen.getByText('Keep')).toBeInTheDocument()
    expect(screen.getByText('Delete')).toBeInTheDocument()
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
    const onConfirm = vi.fn()
    render(() => (
      <ConfirmDialog
        title="Test"
        confirmLabel="Delete"
        danger
        onConfirm={onConfirm}
        onCancel={() => {}}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    // ConfirmButton requires two clicks in danger mode.
    const deleteBtn = screen.getByRole('button', { name: 'Delete' })
    expect(screen.queryByRole('button', { name: 'Confirm?' })).not.toBeInTheDocument()
    // The first click arms the button without confirming.
    fireEvent.click(deleteBtn)
    expect(onConfirm).not.toHaveBeenCalled()
    expect(deleteBtn).toHaveAccessibleName('Confirm?')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))
    expect(onConfirm).toHaveBeenCalledOnce()
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

  it('forwards data-testid to the dialog and confirm/cancel test-ids to their buttons', () => {
    render(() => (
      <ConfirmDialog
        title="Test"
        data-testid="my-dialog"
        cancelTestId="my-cancel"
        confirmTestId="my-confirm"
        onConfirm={() => {}}
        onCancel={() => {}}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    expect(screen.getByTestId('my-dialog')).toBeInTheDocument()
    expect(screen.getByTestId('my-cancel')).toBeInTheDocument()
    expect(screen.getByTestId('my-confirm')).toBeInTheDocument()
  })

  it('submits the primary action through Enter outside danger mode', () => {
    const onConfirm = vi.fn()
    render(() => (
      <ConfirmDialog
        title="Test"
        confirmTestId="my-confirm"
        data-testid="my-dialog"
        onConfirm={onConfirm}
        onCancel={() => {}}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    const form = screen.getByTestId('my-dialog').querySelector('form')!
    fireEvent.submit(form)
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  it('does not confirm a danger action through form submission', () => {
    const onConfirm = vi.fn()
    render(() => (
      <ConfirmDialog
        title="Test"
        confirmLabel="Delete"
        confirmTestId="my-confirm"
        data-testid="my-dialog"
        danger
        onConfirm={onConfirm}
        onCancel={() => {}}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    const form = screen.getByTestId('my-dialog').querySelector('form')!
    fireEvent.submit(form)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('blocks form submission while busy', () => {
    const onConfirm = vi.fn()
    render(() => (
      <ConfirmDialog
        title="Test"
        data-testid="my-dialog"
        busy
        onConfirm={onConfirm}
        onCancel={() => {}}
      >
        <p>Message</p>
      </ConfirmDialog>
    ))
    const form = screen.getByTestId('my-dialog').querySelector('form')!
    fireEvent.submit(form)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('disables cancel and confirm buttons when busy', () => {
    render(() => (
      <ConfirmDialog title="Test" busy onConfirm={() => {}} onCancel={() => {}}>
        <p>Are you sure?</p>
      </ConfirmDialog>
    ))

    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'OK' })).toBeDisabled()
  })

  it('enables cancel and confirm buttons when not busy', () => {
    render(() => (
      <ConfirmDialog title="Test" onConfirm={() => {}} onCancel={() => {}}>
        <p>Are you sure?</p>
      </ConfirmDialog>
    ))

    expect(screen.getByRole('button', { name: 'Cancel' })).not.toBeDisabled()
    expect(screen.getByRole('button', { name: 'OK' })).not.toBeDisabled()
  })

  describe('secondary slot', () => {
    it('renders the secondary button between cancel and primary', () => {
      render(() => (
        <ConfirmDialog
          title="Test"
          confirmLabel="Convert"
          confirmTestId="my-confirm"
          cancelTestId="my-cancel"
          data-testid="my-dialog"
          onConfirm={() => {}}
          onCancel={() => {}}
          secondary={{
            label: 'Close all',
            testId: 'my-secondary',
            onClick: () => {},
          }}
        >
          <p>Message</p>
        </ConfirmDialog>
      ))
      const footer = screen.getByTestId('my-dialog').querySelector('footer')!
      const labels = [...footer.querySelectorAll('button')].map(b => b.textContent?.trim())
      expect(labels).toEqual(['Cancel', 'Close all', 'Convert'])
      expect(screen.getByTestId('my-secondary')).toBeInTheDocument()
    })

    it('calls the secondary action on one click outside danger mode', () => {
      const onSecondary = vi.fn()
      render(() => (
        <ConfirmDialog
          title="Test"
          onConfirm={() => {}}
          onCancel={() => {}}
          secondary={{
            label: 'Close all',
            testId: 'my-secondary',
            onClick: onSecondary,
          }}
        >
          <p>Message</p>
        </ConfirmDialog>
      ))
      fireEvent.click(screen.getByTestId('my-secondary'))
      expect(onSecondary).toHaveBeenCalledOnce()
    })

    it('requires two clicks for a secondary action in danger mode', () => {
      const onSecondary = vi.fn()
      render(() => (
        <ConfirmDialog
          title="Test"
          onConfirm={() => {}}
          onCancel={() => {}}
          secondary={{
            label: 'Close all',
            testId: 'my-secondary',
            onClick: onSecondary,
            danger: true,
          }}
        >
          <p>Message</p>
        </ConfirmDialog>
      ))
      const btn = screen.getByTestId('my-secondary')
      expect(btn).toHaveAccessibleName('Close all')
      fireEvent.click(btn)
      expect(onSecondary).not.toHaveBeenCalled()
      expect(btn).toHaveAccessibleName('Confirm?')
      fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))
      expect(onSecondary).toHaveBeenCalledOnce()
    })

    it('disables the secondary button while busy', () => {
      render(() => (
        <ConfirmDialog
          title="Test"
          busy
          onConfirm={() => {}}
          onCancel={() => {}}
          secondary={{
            label: 'Close all',
            testId: 'my-secondary',
            onClick: () => {},
            danger: true,
          }}
        >
          <p>Message</p>
        </ConfirmDialog>
      ))
      expect(screen.getByTestId('my-secondary')).toBeDisabled()
    })

    it('omits the secondary button when no secondary prop is provided', () => {
      render(() => (
        <ConfirmDialog
          title="Test"
          data-testid="solo-dialog"
          onConfirm={() => {}}
          onCancel={() => {}}
        >
          <p>Message</p>
        </ConfirmDialog>
      ))
      const footer = screen.getByTestId('solo-dialog').querySelector('footer')!
      expect(footer.querySelectorAll('button')).toHaveLength(2)
    })
  })
})
