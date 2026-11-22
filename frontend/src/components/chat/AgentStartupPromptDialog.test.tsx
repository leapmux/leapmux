import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentStartupPromptSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { motion } from '~/styles/tokens'
import { deferred, flush } from '~/test-support/async'
import { AgentStartupPromptDialog } from './AgentStartupPromptDialog'

beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
    this.setAttribute('open', '')
  })
  HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  })
})

afterEach(() => {
  vi.useRealTimers()
})

function samplePrompt(overrides: Partial<Parameters<typeof create<typeof AgentStartupPromptSchema>>[1]> = {}) {
  return create(AgentStartupPromptSchema, {
    promptId: 'prompt-1',
    title: 'macOS Keychain access required',
    description: 'ZCode stores its encrypted account key in the macOS login Keychain.',
    steps: [
      'Click Proceed to trigger the macOS Keychain permission dialog.',
      'Click Always Allow so macOS does not ask again.',
    ],
    note: 'If this Worker runs on another Mac, the dialog opens on that Mac.',
    confirmLabel: 'Proceed',
    cancelLabel: 'Cancel',
    ...overrides,
  })
}

function noopRespond() {
  return Promise.resolve()
}

describe('AgentStartupPromptDialog', () => {
  it('renders title, description, numbered steps, note, and custom button labels', () => {
    render(() => (
      <AgentStartupPromptDialog
        prompt={samplePrompt({ confirmLabel: 'Allow Keychain', cancelLabel: 'Stop' })}
        onRespond={noopRespond}
      />
    ))

    expect(screen.getByTestId('agent-startup-prompt-dialog')).toBeInTheDocument()
    expect(screen.getByText('macOS Keychain access required')).toBeInTheDocument()
    expect(screen.getByText('ZCode stores its encrypted account key in the macOS login Keychain.')).toBeInTheDocument()
    const items = screen.getAllByRole('listitem')
    expect(items).toHaveLength(2)
    expect(items[0]).toHaveTextContent('Click Proceed to trigger the macOS Keychain permission dialog.')
    expect(items[1]).toHaveTextContent('Click Always Allow so macOS does not ask again.')
    expect(screen.getByText('If this Worker runs on another Mac, the dialog opens on that Mac.')).toBeInTheDocument()
    expect(screen.getByTestId('agent-startup-prompt-proceed')).toHaveTextContent('Allow Keychain')
    expect(screen.getByTestId('agent-startup-prompt-cancel')).toHaveTextContent('Stop')
  })

  it('defaults button labels and omits steps list and note when empty', () => {
    render(() => (
      <AgentStartupPromptDialog
        prompt={samplePrompt({
          steps: [],
          note: '',
          confirmLabel: '',
          cancelLabel: '',
        })}
        onRespond={noopRespond}
      />
    ))

    expect(screen.queryByRole('list')).not.toBeInTheDocument()
    expect(screen.queryByText(/another Mac/)).not.toBeInTheDocument()
    expect(screen.getByTestId('agent-startup-prompt-proceed')).toHaveTextContent('Proceed')
    expect(screen.getByTestId('agent-startup-prompt-cancel')).toHaveTextContent('Cancel')
  })

  it('calls onRespond(true), sets busy, and ignores duplicate clicks while pending', async () => {
    const pending = deferred<void>()
    const onRespond = vi.fn(() => pending.promise)

    render(() => (
      <AgentStartupPromptDialog
        prompt={samplePrompt()}
        onRespond={onRespond}
      />
    ))

    const proceedBtn = screen.getByTestId('agent-startup-prompt-proceed')
    const cancelBtn = screen.getByTestId('agent-startup-prompt-cancel')
    fireEvent.click(proceedBtn)
    expect(onRespond).toHaveBeenCalledTimes(1)
    expect(onRespond).toHaveBeenCalledWith(true)
    expect(proceedBtn).toBeDisabled()
    expect(cancelBtn).toBeDisabled()

    fireEvent.click(proceedBtn)
    fireEvent.submit(proceedBtn.closest('form')!)
    expect(onRespond).toHaveBeenCalledTimes(1)

    pending.resolve()
    await flush()
  })

  it('calls onRespond(false) when Cancel is clicked', () => {
    const onRespond = vi.fn(async () => {})
    render(() => (
      <AgentStartupPromptDialog
        prompt={samplePrompt()}
        onRespond={onRespond}
      />
    ))

    fireEvent.click(screen.getByTestId('agent-startup-prompt-cancel'))
    expect(onRespond).toHaveBeenCalledTimes(1)
    expect(onRespond).toHaveBeenCalledWith(false)
  })

  it('calls onRespond(false) when dismissed via Escape, close button, or backdrop click', () => {
    vi.useFakeTimers()
    for (const mode of ['escape', 'close-button', 'backdrop'] as const) {
      const onRespond = vi.fn(async () => {})
      const { unmount } = render(() => (
        <AgentStartupPromptDialog
          prompt={samplePrompt()}
          onRespond={onRespond}
        />
      ))
      const dialog = screen.getByTestId('agent-startup-prompt-dialog')
      if (mode === 'escape') {
        fireEvent(dialog, new Event('cancel', { cancelable: true }))
      }
      else if (mode === 'close-button') {
        fireEvent.click(screen.getByRole('button', { name: 'Close' }))
      }
      else {
        // Dialog requires both pointerDown and click outside its bounding rect.
        fireEvent.pointerDown(dialog, { clientX: -10, clientY: -10 })
        fireEvent.click(dialog, { clientX: -10, clientY: -10 })
      }
      vi.advanceTimersByTime(motion.fast)
      expect(onRespond, mode).toHaveBeenCalledTimes(1)
      expect(onRespond, mode).toHaveBeenCalledWith(false)
      unmount()
    }
  })

  it('clears busy and remounts visibly when onRespond rejects after a dismiss-triggered cancel', async () => {
    vi.useFakeTimers()
    const onRespond = vi.fn()
      .mockRejectedValueOnce(new Error('network error'))
      .mockResolvedValueOnce(undefined)

    render(() => (
      <AgentStartupPromptDialog
        prompt={samplePrompt()}
        onRespond={onRespond}
      />
    ))

    const initialShowCount = (HTMLDialogElement.prototype.showModal as ReturnType<typeof vi.fn>).mock.calls.length
    const dialog = screen.getByTestId('agent-startup-prompt-dialog')
    fireEvent(dialog, new Event('cancel', { cancelable: true }))
    expect(dialog).toHaveAttribute('data-closing', '')
    vi.advanceTimersByTime(motion.fast)
    await flush()

    expect(onRespond).toHaveBeenCalledTimes(1)
    expect(onRespond).toHaveBeenCalledWith(false)
    // Failed dismiss re-keys ConfirmDialog so showModal runs again and the
    // remounted dialog is no longer stuck in data-closing.
    expect((HTMLDialogElement.prototype.showModal as ReturnType<typeof vi.fn>).mock.calls.length).toBeGreaterThan(initialShowCount)
    const remounted = screen.getByTestId('agent-startup-prompt-dialog')
    expect(remounted).not.toBe(dialog)
    expect(remounted).not.toHaveAttribute('data-closing')
    const proceedBtn = screen.getByTestId('agent-startup-prompt-proceed')
    expect(proceedBtn).not.toBeDisabled()
    fireEvent.click(proceedBtn)
    await flush()
    expect(onRespond).toHaveBeenCalledTimes(2)
    expect(onRespond).toHaveBeenLastCalledWith(true)
  })
})
