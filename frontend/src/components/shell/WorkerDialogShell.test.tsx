import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { tall as tallClass, wide as wideClass } from '~/components/common/Dialog.css'
import { errorText } from '~/styles/shared.css'
import { DialogFormFooter, WorkerDialogShell } from './WorkerDialogShell'

function renderShellWithFormFooter(overrides: Partial<{
  title: string
  submitting: boolean
  submitDisabled: boolean
  submitLabel: string
  submittingLabel: string
  error: string | null
  onSubmit: (e: Event) => void
  onClose: () => void
  compact: boolean
}> = {}) {
  const onSubmit = overrides.onSubmit ?? vi.fn()
  const onClose = overrides.onClose ?? vi.fn()
  const result = render(() => (
    <WorkerDialogShell
      title={overrides.title ?? 'New thingy'}
      submitting={overrides.submitting ?? false}
      error={overrides.error ?? null}
      onSubmit={onSubmit}
      onClose={onClose}
      compact={overrides.compact}
      footer={(
        <DialogFormFooter
          submitting={overrides.submitting ?? false}
          submitDisabled={overrides.submitDisabled}
          submitLabel={overrides.submitLabel ?? 'Create'}
          submittingLabel={overrides.submittingLabel}
          onClose={onClose}
        />
      )}
    >
      <div data-testid="body">payload</div>
    </WorkerDialogShell>
  ))
  return { ...result, onSubmit, onClose }
}

describe('workerDialogShell', () => {
  it('renders the title and body content', () => {
    renderShellWithFormFooter({ title: 'New thingy' })
    expect(screen.getByText('New thingy')).toBeInTheDocument()
    expect(screen.getByTestId('body')).toBeInTheDocument()
  })

  it('renders error text when error is non-empty', () => {
    renderShellWithFormFooter({ error: 'something broke' })
    expect(screen.getByText('something broke')).toBeInTheDocument()
  })

  it('hides the error row for null/undefined/empty error', () => {
    const { unmount } = renderShellWithFormFooter({ error: null })
    // The errorText slot exists only inside the `<Show when={error}>`;
    // querying for the rendered class beats grepping innerHTML for a
    // substring (which would match an unrelated `errorTextSomething`).
    expect(document.querySelector(`.${errorText}`)).toBeNull()
    unmount()
  })

  it('applies the tall/wide CSS classes by default', () => {
    renderShellWithFormFooter()
    const dialog = document.querySelector('dialog')!
    expect(dialog.classList.contains(tallClass)).toBe(true)
    expect(dialog.classList.contains(wideClass)).toBe(true)
  })

  it('omits the tall/wide classes when the caller opts out via compact', () => {
    renderShellWithFormFooter({ compact: true })
    const dialog = document.querySelector('dialog')!
    expect(dialog.classList.contains(tallClass)).toBe(false)
    expect(dialog.classList.contains(wideClass)).toBe(false)
  })

  it('clicking Cancel calls onClose; submitting the form fires onSubmit', async () => {
    const onClose = vi.fn()
    const onSubmit = vi.fn()
    renderShellWithFormFooter({ onClose, onSubmit })

    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalledTimes(1)

    const form = document.querySelector('form')!
    await fireEvent.submit(form)
    expect(onSubmit).toHaveBeenCalledTimes(1)
  })

  it('skips the <form> wrapper when onSubmit is omitted (custom-footer dialogs)', () => {
    render(() => (
      <WorkerDialogShell
        title="Confirm"
        submitting={false}
        error={null}
        onClose={() => {}}
        footer={(
          <button type="button" data-testid="custom-action">
            Custom Action
          </button>
        )}
      >
        <div>body</div>
      </WorkerDialogShell>
    ))
    expect(screen.getByTestId('custom-action')).toBeInTheDocument()
    // No form means no incidental Enter-key submit reaching a stray
    // button[type=submit] — the caller owns its own action shape.
    expect(document.querySelector('form')).toBeNull()
  })

  it('propagates submitting busy overlay regardless of footer shape', () => {
    render(() => (
      <WorkerDialogShell
        title="Confirm"
        submitting={true}
        error={null}
        onClose={() => {}}
        footer={<button type="button">Action</button>}
      >
        <div>body</div>
      </WorkerDialogShell>
    ))
    expect(document.querySelector('[data-busy]')).toBeTruthy()
  })
})

describe('dialogFormFooter', () => {
  it('disables Submit when submitDisabled is true', () => {
    renderShellWithFormFooter({ submitDisabled: true })
    const submit = screen.getByRole('button', { name: 'Create' }) as HTMLButtonElement
    expect(submit.disabled).toBe(true)
  })

  it('while submitting: shows submittingLabel, disables both buttons, sets dialog busy', () => {
    renderShellWithFormFooter({ submitting: true, submittingLabel: 'Creating...' })
    const cancel = screen.getByRole('button', { name: 'Cancel' }) as HTMLButtonElement
    const submit = screen.getByRole('button', { name: /Creating/ }) as HTMLButtonElement
    expect(cancel.disabled).toBe(true)
    expect(submit.disabled).toBe(true)
    expect(document.querySelector('[data-busy]')).toBeTruthy()
  })

  it('defaults submittingLabel to "<submitLabel>..." when omitted', () => {
    renderShellWithFormFooter({ submitting: true, submitLabel: 'Save' })
    expect(screen.getByRole('button', { name: /Save\.\.\./ })).toBeInTheDocument()
  })
})
