/// <reference types="vitest/globals" />
import type { Timestamp } from '@bufbuild/protobuf/wkt'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { Code, ConnectError } from '@connectrpc/connect'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'

import { RegisterWorkerDialog } from './RegisterWorkerDialog'

// Mocks ----------------------------------------------------------------

const mockCreate = vi.fn<() => Promise<{ registrationKey: string, expiresAt: Timestamp }>>()
const mockExtend = vi.fn<(args: { registrationKey: string }) => Promise<{ expiresAt: Timestamp }>>()
const mockRevoke = vi.fn<(args: { registrationKey: string }) => Promise<unknown>>()
const mockEmail = vi.fn<(args: { registrationKey: string, command: string }) => Promise<unknown>>()

vi.mock('~/api/clients', () => ({
  workerClient: {
    createRegistrationKey: (...a: Parameters<typeof mockCreate>) => mockCreate(...a),
    extendRegistrationKey: (...a: Parameters<typeof mockExtend>) => mockExtend(...a),
    // A vitest spy attaches its own handlers to each promise it returns (to
    // record `settledResults`), so a rejection straight from `mockRevoke`
    // never counts as unhandled. `.then` gives the dialog a derived promise
    // that only the dialog can handle.
    revokeRegistrationKey: (...a: Parameters<typeof mockRevoke>) => mockRevoke(...a).then(r => r),
    emailRegistrationInstructions: (...a: Parameters<typeof mockEmail>) => mockEmail(...a),
  },
}))

const mockAuthUser = vi.fn<() => { email?: string, emailVerified?: boolean } | null>()
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockAuthUser(),
    loading: () => false,
    login: vi.fn(),
    logout: vi.fn(),
    setAuth: vi.fn(),
    isAuthenticated: () => mockAuthUser() != null,
  }),
}))

const mockSoloMode = vi.fn<() => boolean>()
const mockWorkerHubUrl = vi.fn<() => string>()
const mockEmailEnabled = vi.fn<() => boolean>()
vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => mockSoloMode(),
  getWorkerHubUrl: () => mockWorkerHubUrl(),
  isEmailEnabled: () => mockEmailEnabled(),
  isSystemInfoLoaded: () => true,
  isCaptchaEnabled: () => false,
}))

// Helpers --------------------------------------------------------------

beforeAll(() => {
  // jsdom doesn't implement <dialog> — Dialog calls showModal()/close() so
  // we must stub them to make/unmake the open attribute (matches the
  // pattern other dialog specs in this repo use, e.g. AddTunnelDialog).
  HTMLDialogElement.prototype.showModal = vi.fn(function (this: HTMLDialogElement) {
    this.setAttribute('open', '')
  })
  HTMLDialogElement.prototype.close = vi.fn(function (this: HTMLDialogElement) {
    this.removeAttribute('open')
  })

  // Stub clipboard so useCopyButton can succeed.
  Object.defineProperty(navigator, 'clipboard', {
    value: {
      writeText: vi.fn().mockResolvedValue(undefined),
    },
    configurable: true,
  })
  // jsdom defaults window.location.origin to about:blank in some setups —
  // override so the rendered command embeds a stable hub URL.
  Object.defineProperty(window, 'location', {
    value: { ...window.location, origin: 'http://hub.test' },
    configurable: true,
  })
})

beforeEach(() => {
  vi.clearAllMocks()
  mockAuthUser.mockReturnValue({ email: 'me@example.com', emailVerified: true })
  mockSoloMode.mockReturnValue(false)
  mockWorkerHubUrl.mockReturnValue('')
  mockEmailEnabled.mockReturnValue(true)
  mockCreate.mockResolvedValue({
    registrationKey: 'KEY-ABC',
    expiresAt: timestampFromDate(new Date(Date.now() + 5 * 60_000)),
  })
  mockExtend.mockResolvedValue({
    expiresAt: timestampFromDate(new Date(Date.now() + 5 * 60_000)),
  })
  mockRevoke.mockResolvedValue({})
  mockEmail.mockResolvedValue({})
})

afterEach(() => {
  vi.useRealTimers()
})

function renderDialog(onClose = vi.fn()) {
  const { unmount } = render(() => <RegisterWorkerDialog onClose={onClose} />)
  return { onClose, unmount }
}

// Tests ----------------------------------------------------------------

describe('RegisterWorkerDialog', () => {
  it('revokes a key that arrives after the dialog unmounts', async () => {
    const creation = deferred<Awaited<ReturnType<typeof mockCreate>>>()
    mockCreate.mockReturnValueOnce(creation.promise)
    const { unmount } = renderDialog()
    await waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())
    unmount()
    creation.resolve({
      registrationKey: 'LATE-KEY',
      expiresAt: timestampFromDate(new Date(Date.now() + 300_000)),
    })
    await waitFor(() => expect(mockRevoke).toHaveBeenCalledExactlyOnceWith({ registrationKey: 'LATE-KEY' }))
  })

  it('handles a rejected late revocation without an unhandled promise', async () => {
    const creation = deferred<Awaited<ReturnType<typeof mockCreate>>>()
    mockCreate.mockReturnValueOnce(creation.promise)
    mockRevoke.mockRejectedValueOnce(new ConnectError('revocation unavailable', Code.Unavailable))
    const unhandled: unknown[] = []
    const onUnhandled = (reason: unknown) => unhandled.push(reason)
    process.on('unhandledRejection', onUnhandled)
    try {
      const { unmount } = renderDialog()
      await waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())
      unmount()
      creation.resolve({
        registrationKey: 'LATE-REFUSAL',
        expiresAt: timestampFromDate(new Date(Date.now() + 300_000)),
      })
      await waitFor(() => expect(mockRevoke).toHaveBeenCalledExactlyOnceWith({ registrationKey: 'LATE-REFUSAL' }))
      await new Promise(resolve => setImmediate(resolve))
      expect(unhandled).toEqual([])
    }
    finally {
      process.off('unhandledRejection', onUnhandled)
    }
  })

  it('keeps a reopened dialog independent of an earlier pending key', async () => {
    const creation = deferred<Awaited<ReturnType<typeof mockCreate>>>()
    mockCreate.mockReturnValueOnce(creation.promise)
    const previous = renderDialog()
    await waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())
    previous.unmount()
    mockCreate.mockResolvedValueOnce({
      registrationKey: 'NEW-DIALOG-KEY',
      expiresAt: timestampFromDate(new Date(Date.now() + 300_000)),
    })
    renderDialog()
    await screen.findByTestId('registration-command')
    creation.resolve({
      registrationKey: 'OLD-DIALOG-KEY',
      expiresAt: timestampFromDate(new Date(Date.now() + 300_000)),
    })
    await waitFor(() => expect(mockRevoke).toHaveBeenCalledExactlyOnceWith({ registrationKey: 'OLD-DIALOG-KEY' }))
    expect(screen.getByTestId('registration-command')).toHaveTextContent('NEW-DIALOG-KEY')
  })

  it('refuses email and clipboard actions when command validation fails', async () => {
    mockWorkerHubUrl.mockReturnValue('https://hub.test/unsafe\nendpoint')
    renderDialog()
    await screen.findByText('The Hub address or registration key cannot form a safe command. Check the Hub configuration or generate a new key.')
    const email = screen.getByTestId('email-registration-instructions')
    expect(email).toBeDisabled()
    fireEvent.click(email)
    fireEvent.click(screen.getByTestId('copy-registration-command'))
    fireEvent.click(screen.getByTestId('copy-start-command'))
    expect(mockEmail).not.toHaveBeenCalled()
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })

  it('mints a key on mount and renders the full command', async () => {
    renderDialog()

    await waitFor(() => {
      expect(mockCreate).toHaveBeenCalledOnce()
    })
    const block = await screen.findByTestId('registration-command')
    expect(block.textContent).toContain('leapmux worker register --hub \'http://hub.test\' --registration-key \'KEY-ABC\'')
  })

  it('copies the rendered command to the clipboard', async () => {
    renderDialog()
    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>

    await screen.findByTestId('registration-command')
    fireEvent.click(screen.getByTestId('copy-registration-command'))

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith(
        'leapmux worker register --hub \'http://hub.test\' --registration-key \'KEY-ABC\'',
      )
    })
  })

  it('copies startup separately after displaying successful-registration guidance', async () => {
    renderDialog()
    await screen.findByTestId('start-command')
    expect(screen.getByText('2. After registration succeeds, start the worker.')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('copy-start-command'))
    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith('leapmux worker start'))
    expect(navigator.clipboard.writeText).toHaveBeenCalledOnce()
  })

  it('copies and emails the selected PowerShell command with literal apostrophes', async () => {
    mockWorkerHubUrl.mockReturnValue('unix:/tmp/a\'s hub.sock')
    mockCreate.mockResolvedValue({
      registrationKey: 'it\'s a key',
      expiresAt: timestampFromDate(new Date(Date.now() + 300_000)),
    })
    renderDialog()
    await screen.findByTestId('registration-command')
    fireEvent.click(screen.getByRole('radio', { name: 'PowerShell' }))
    const command = screen.getByTestId('registration-command').textContent!
    expect(command).toContain('System.Diagnostics.ProcessStartInfo')
    expect(command).toContain(`$info.Arguments = 'worker register --hub "unix:/tmp/a''s hub.sock" --registration-key "it''s a key"'`)
    fireEvent.click(screen.getByTestId('copy-registration-command'))
    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith(command))
    fireEvent.click(screen.getByTestId('email-registration-instructions'))
    await waitFor(() => expect(mockEmail).toHaveBeenCalledWith({ registrationKey: 'it\'s a key', command }))
  })

  it('reports an empty key response without leaving generation pending', async () => {
    mockCreate.mockResolvedValue({
      registrationKey: '',
      expiresAt: timestampFromDate(new Date()),
    })
    renderDialog()
    expect(await screen.findByText('The Hub did not return a registration key. Close this dialog and try again.')).toBeInTheDocument()
    expect(screen.queryByText('Generating registration key…')).not.toBeInTheDocument()
    expect(screen.queryByTestId('copy-registration-command')).not.toBeInTheDocument()
    expect(screen.queryByTestId('email-registration-instructions')).not.toBeInTheDocument()
  })

  it('reports key creation failure without displaying an executable command', async () => {
    mockCreate.mockRejectedValue(new ConnectError('registration disabled', Code.PermissionDenied))
    renderDialog()
    expect(await screen.findByText(/Failed to create registration key:.*registration disabled/)).toBeInTheDocument()
    expect(screen.queryByText('Generating registration key…')).not.toBeInTheDocument()
    expect(screen.queryByTestId('registration-command')).not.toBeInTheDocument()
    expect(mockEmail).not.toHaveBeenCalled()
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })

  it('soft-deletes the key when the dialog unmounts', async () => {
    const { unmount } = renderDialog()
    await screen.findByTestId('registration-command')

    unmount()

    await waitFor(() => {
      expect(mockRevoke).toHaveBeenCalledWith({ registrationKey: 'KEY-ABC' })
    })
  })

  it('swallows the not-found answer for a key that a registration already consumed', async () => {
    // The hub answers NotFound once a worker registered with the key, so this
    // is the normal answer on close, and it must not surface as an unhandled
    // rejection.
    const unhandled: unknown[] = []
    const onUnhandled = (reason: unknown) => unhandled.push(reason)
    process.on('unhandledRejection', onUnhandled)
    try {
      mockRevoke.mockRejectedValue(new ConnectError('registration key not found', Code.NotFound))
      const { unmount } = renderDialog()
      await screen.findByTestId('registration-command')

      unmount()

      expect(mockRevoke).toHaveBeenCalledExactlyOnceWith({ registrationKey: 'KEY-ABC' })
      // One macrotask lets the rejection reach a listener if nothing caught it.
      await new Promise(resolve => setImmediate(resolve))
      expect(unhandled).toEqual([])
    }
    finally {
      process.off('unhandledRejection', onUnhandled)
    }
  })

  it('does NOT extend while plenty of time remains', async () => {
    // Fake timers must be installed *before* the mount so the interval
    // attaches to the controllable clock instead of the real one.
    vi.useFakeTimers()

    // 5 minutes remaining — far above the 2-min threshold the dialog
    // uses to decide whether to extend.
    mockCreate.mockResolvedValueOnce({
      registrationKey: 'KEY-FRESH',
      expiresAt: timestampFromDate(new Date(Date.now() + 5 * 60_000)),
    })

    renderDialog()
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())

    // Advance 60s — two ticks of the 30s extension loop.
    await vi.advanceTimersByTimeAsync(60_000)
    expect(mockExtend).not.toHaveBeenCalled()
  })

  it('extends once the key drops inside the 2-minute window', async () => {
    vi.useFakeTimers()

    // 90 seconds remaining — already inside the threshold, so the very
    // first 30s tick should call extend.
    mockCreate.mockResolvedValueOnce({
      registrationKey: 'KEY-TIGHT',
      expiresAt: timestampFromDate(new Date(Date.now() + 90_000)),
    })

    renderDialog()
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledOnce())

    await vi.advanceTimersByTimeAsync(30_000)
    expect(mockExtend).toHaveBeenCalledWith({ registrationKey: 'KEY-TIGHT' })
  })

  it('disables the email button when the user has no verified email', async () => {
    mockAuthUser.mockReturnValue({ email: 'me@example.com', emailVerified: false })
    renderDialog()

    const btn = await screen.findByTestId('email-registration-instructions') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    fireEvent.click(btn)
    expect(mockEmail).not.toHaveBeenCalled()
  })

  it('hides the email button entirely in solo mode', async () => {
    // Solo mode: bootstrapped user has no email and no SMTP is configured.
    // A disabled button with a "verify your email" tooltip would be
    // misleading, so the button is omitted from the action row.
    mockSoloMode.mockReturnValue(true)
    renderDialog()

    await screen.findByTestId('registration-command')
    expect(screen.queryByTestId('email-registration-instructions')).toBeNull()
  })

  it('hides the email button when the hub has no SMTP configured', async () => {
    // Hub mode without smtp_host: the corresponding RPC returns
    // FailedPrecondition. Surfacing a disabled-on-the-frontend button
    // would just confuse users — match the solo-mode behavior and omit
    // the button entirely.
    mockEmailEnabled.mockReturnValue(false)
    renderDialog()

    await screen.findByTestId('registration-command')
    expect(screen.queryByTestId('email-registration-instructions')).toBeNull()
  })

  it('uses the hub-advertised worker URL when present (desktop unix-socket case)', async () => {
    // The desktop app's webview origin is `tauri://localhost`, which is
    // unusable as a `--hub` value. The backend reports the actual local
    // listener URL (unix:/...) and the dialog must prefer it.
    mockWorkerHubUrl.mockReturnValue('unix:/tmp/leapmux-hub.sock')
    renderDialog()

    const block = await screen.findByTestId('registration-command')
    expect(block.textContent).toContain('--hub \'unix:/tmp/leapmux-hub.sock\'')
    expect(block.textContent).not.toContain('hub.test')
  })

  it('sends the email and reports the destination on success', async () => {
    renderDialog()
    const btn = await screen.findByTestId('email-registration-instructions')

    fireEvent.click(btn)

    await waitFor(() => {
      expect(mockEmail).toHaveBeenCalledWith({
        registrationKey: 'KEY-ABC',
        command: 'leapmux worker register --hub \'http://hub.test\' --registration-key \'KEY-ABC\'',
      })
    })
    expect(await screen.findByText(/Sent to me@example.com/)).toBeInTheDocument()
  })
})
