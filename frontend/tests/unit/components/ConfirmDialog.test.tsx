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
    expect(deleteBtn).toBeInTheDocument()
    // First click arms it, doesn't trigger confirm
    fireEvent.click(deleteBtn)
    expect(screen.getByText('Confirm?')).toBeInTheDocument()
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

  it('non-danger primary submits via Enter (form submit handler)', () => {
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

  it('danger primary does NOT fire on form submit (ConfirmButton must arm)', () => {
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

  it('busy disables form submit (Enter cannot bypass)', () => {
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

    it('non-danger secondary fires on a single click', () => {
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

    it('danger secondary requires two clicks (ConfirmButton arming)', () => {
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
      fireEvent.click(btn)
      expect(onSecondary).not.toHaveBeenCalled()
      fireEvent.click(btn)
      expect(onSecondary).toHaveBeenCalledOnce()
    })

    it('busy disables the secondary button', () => {
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
