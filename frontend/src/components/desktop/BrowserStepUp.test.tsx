import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { BrowserStepUp } from './BrowserStepUp'

/**
 * The ceremony seams: the sidecar's two commands and its one result event per
 * ceremony. `onEvent` hands the callback the emitted payload, never the Tauri
 * event envelope.
 */
const bridgeMocks = vi.hoisted(() => ({
  openExternalUrl: vi.fn<(url: string) => Promise<void>>(),
  startBrowserAuth: vi.fn<() => Promise<{ ceremonyId: string, authorizeUrl: string }>>(),
  cancelBrowserAuth: vi.fn<(ceremonyId: string) => Promise<void>>(),
  handlers: [] as ((...args: unknown[]) => void)[],
  emit(payload: { ceremonyId: string, ok: boolean, failure: string }): void {
    for (const handler of [...bridgeMocks.handlers])
      handler(payload)
  },
}))

vi.mock('~/api/platformBridge', () => ({
  openExternalUrl: (url: string) => bridgeMocks.openExternalUrl(url),
  platformBridge: {
    startBrowserAuth: () => bridgeMocks.startBrowserAuth(),
    cancelBrowserAuth: (ceremonyId: string) => bridgeMocks.cancelBrowserAuth(ceremonyId),
    onEvent: (_event: string, handler: (...args: unknown[]) => void): Promise<() => void> => {
      bridgeMocks.handlers.push(handler)
      return Promise.resolve(vi.fn(() => {
        const index = bridgeMocks.handlers.indexOf(handler)
        if (index >= 0)
          bridgeMocks.handlers.splice(index, 1)
      }))
    },
  },
}))

const mockRefreshUser = vi.hoisted(() => vi.fn<() => Promise<void>>())
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    loading: () => false,
    refreshUser: () => mockRefreshUser(),
  }),
}))

const copyTextToClipboard = vi.hoisted(() => vi.fn<(text: string) => Promise<boolean>>())
vi.mock('~/lib/clipboard', () => ({
  copyTextToClipboard: (text: string) => copyTextToClipboard(text),
}))

const START = {
  ceremonyId: 'c-1',
  authorizeUrl: 'https://hub.example/authorize',
}

function renderStepUp() {
  const onElevated = vi.fn()
  const result = render(() => <BrowserStepUp onElevated={onElevated} />)
  return { ...result, onElevated }
}

async function startCeremony() {
  fireEvent.click(await screen.findByRole('button', { name: 'Verify in your browser' }))
  await vi.waitFor(() => expect(bridgeMocks.openExternalUrl).toHaveBeenCalled())
}

describe('BrowserStepUp', () => {
  beforeEach(() => {
    bridgeMocks.openExternalUrl.mockReset()
    bridgeMocks.openExternalUrl.mockResolvedValue(undefined)
    bridgeMocks.startBrowserAuth.mockReset()
    bridgeMocks.startBrowserAuth.mockResolvedValue(START)
    bridgeMocks.cancelBrowserAuth.mockReset()
    bridgeMocks.cancelBrowserAuth.mockResolvedValue(undefined)
    bridgeMocks.handlers.length = 0
    copyTextToClipboard.mockReset()
    copyTextToClipboard.mockResolvedValue(true)
    mockRefreshUser.mockReset()
    mockRefreshUser.mockResolvedValue(undefined)
  })

  it('shows the reason and keeps the button on a failure', async () => {
    const { onElevated } = renderStepUp()
    await startCeremony()

    bridgeMocks.emit({ ceremonyId: 'c-1', ok: false, failure: 'the code expired' })

    expect(await screen.findByText('the code expired')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Verify in your browser' })).toBeInTheDocument()
    expect(onElevated).not.toHaveBeenCalled()
    expect(mockRefreshUser).not.toHaveBeenCalled()
  })

  it('opens the same URL again and starts no second ceremony', async () => {
    renderStepUp()
    await startCeremony()

    fireEvent.click(await screen.findByRole('button', { name: 'Open again' }))

    await vi.waitFor(() => expect(bridgeMocks.openExternalUrl).toHaveBeenCalledTimes(2))
    expect(bridgeMocks.openExternalUrl).toHaveBeenNthCalledWith(2, 'https://hub.example/authorize')
    expect(bridgeMocks.startBrowserAuth).toHaveBeenCalledTimes(1)
  })

  it('copies the ceremony link through the clipboard gateway', async () => {
    // A direct `navigator.clipboard` write would reach this spy instead of
    // the gateway, which alone adds the fallback and the failure toast.
    const directWrite = vi.fn()
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: directWrite },
      configurable: true,
    })
    renderStepUp()
    await startCeremony()

    fireEvent.click(await screen.findByRole('button', { name: 'Copy link' }))

    await vi.waitFor(() => expect(copyTextToClipboard).toHaveBeenCalledExactlyOnceWith('https://hub.example/authorize'))
    expect(directWrite).not.toHaveBeenCalled()
  })
})
