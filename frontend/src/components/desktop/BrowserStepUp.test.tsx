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

  // Every unmount aborts the ceremony -- also the one the app-root
  // ErrorBoundary causes when it unmounts the host above this component.
  it('cancels the ceremony when the host unmounts during the wait', async () => {
    const { unmount } = renderStepUp()
    await startCeremony()

    unmount()

    await vi.waitFor(() => expect(bridgeMocks.cancelBrowserAuth).toHaveBeenCalledExactlyOnceWith('c-1'))
  })
})
