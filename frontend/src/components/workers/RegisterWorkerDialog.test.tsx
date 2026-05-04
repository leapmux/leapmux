/// <reference types="vitest/globals" />
import type { Timestamp } from '@bufbuild/protobuf/wkt'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { RegisterWorkerDialog } from './RegisterWorkerDialog'

// Mocks ----------------------------------------------------------------

const mockCreate = vi.fn<() => Promise<{ registrationKey: string, expiresAt: Timestamp }>>()
const mockExtend = vi.fn<(args: { registrationKey: string }) => Promise<{ expiresAt: Timestamp }>>()
const mockDelete = vi.fn<(args: { registrationKey: string }) => Promise<unknown>>()
const mockEmail = vi.fn<(args: { registrationKey: string, command: string }) => Promise<unknown>>()

vi.mock('~/api/clients', () => ({
  workerClient: {
    createRegistrationKey: (...a: Parameters<typeof mockCreate>) => mockCreate(...a),
    extendRegistrationKey: (...a: Parameters<typeof mockExtend>) => mockExtend(...a),
    deleteRegistrationKey: (...a: Parameters<typeof mockDelete>) => mockDelete(...a),
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
vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => mockSoloMode(),
  getWorkerHubUrl: () => mockWorkerHubUrl(),
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
    value: { writeText: vi.fn().mockResolvedValue(undefined) },
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
  mockCreate.mockResolvedValue({
    registrationKey: 'KEY-ABC',
    expiresAt: timestampFromDate(new Date(Date.now() + 5 * 60_000)),
  })
  mockExtend.mockResolvedValue({
    expiresAt: timestampFromDate(new Date(Date.now() + 5 * 60_000)),
  })
  mockDelete.mockResolvedValue({})
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

describe('registerWorkerDialog', () => {
  it('mints a key on mount and renders the full command', async () => {
    renderDialog()

    await waitFor(() => {
      expect(mockCreate).toHaveBeenCalledOnce()
    })
    const block = await screen.findByTestId('registration-command')
    expect(block.textContent).toContain('leapmux worker --hub http://hub.test --registration-key KEY-ABC')
  })

  it('copies the rendered command to the clipboard', async () => {
    renderDialog()
    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>

    await screen.findByTestId('registration-command')
    fireEvent.click(screen.getByTestId('copy-registration-command'))

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith(
        'leapmux worker --hub http://hub.test --registration-key KEY-ABC',
      )
    })
  })

  it('soft-deletes the key when the dialog unmounts', async () => {
    const { unmount } = renderDialog()
    await screen.findByTestId('registration-command')

    unmount()

    await waitFor(() => {
      expect(mockDelete).toHaveBeenCalledWith({ registrationKey: 'KEY-ABC' })
    })
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

  it('uses the hub-advertised worker URL when present (desktop unix-socket case)', async () => {
    // The desktop app's webview origin is `tauri://localhost`, which is
    // unusable as a `--hub` value. The backend reports the actual local
    // listener URL (unix:/...) and the dialog must prefer it.
    mockWorkerHubUrl.mockReturnValue('unix:/tmp/leapmux-hub.sock')
    renderDialog()

    const block = await screen.findByTestId('registration-command')
    expect(block.textContent).toContain('--hub unix:/tmp/leapmux-hub.sock')
    expect(block.textContent).not.toContain('hub.test')
  })

  it('sends the email and reports the destination on success', async () => {
    renderDialog()
    const btn = await screen.findByTestId('email-registration-instructions')

    fireEvent.click(btn)

    await waitFor(() => {
      expect(mockEmail).toHaveBeenCalledWith({
        registrationKey: 'KEY-ABC',
        command: 'leapmux worker --hub http://hub.test --registration-key KEY-ABC',
      })
    })
    expect(await screen.findByText(/Sent to me@example.com/)).toBeInTheDocument()
  })
})
