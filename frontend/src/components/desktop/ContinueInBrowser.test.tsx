import { MemoryRouter, Route } from '@solidjs/router'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ContinueInBrowser } from './ContinueInBrowser'

const mockOpenExternalUrl = vi.hoisted(() => vi.fn<(url: string) => Promise<void>>())
vi.mock('~/api/platformBridge', () => ({
  desktopHub: () => ({ kind: 'remote', url: 'https://hub.example' }),
  openExternalUrl: (url: string) => mockOpenExternalUrl(url),
}))

const mockLoadSystemInfo = vi.hoisted(() => vi.fn<(force?: boolean) => Promise<void>>())
vi.mock('~/lib/systemInfo', () => ({
  loadSystemInfo: (force?: boolean) => mockLoadSystemInfo(force),
}))

const mockRefreshUser = vi.hoisted(() => vi.fn<() => Promise<void>>())
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    loading: () => false,
    refreshUser: () => mockRefreshUser(),
  }),
}))

/**
 * `currentPath` reads `window.location`, which the tests set per case; the
 * memory history only has to match the route below.
 */
function renderContinue() {
  return render(() => (
    <MemoryRouter>
      <Route path="/" component={ContinueInBrowser} />
    </MemoryRouter>
  ))
}

describe('ContinueInBrowser', () => {
  beforeEach(() => {
    window.history.replaceState({}, '', '/')
    mockOpenExternalUrl.mockReset()
    mockOpenExternalUrl.mockResolvedValue(undefined)
    mockLoadSystemInfo.mockReset()
    mockLoadSystemInfo.mockResolvedValue(undefined)
    mockRefreshUser.mockReset()
    mockRefreshUser.mockResolvedValue(undefined)
  })

  it('opens the Hub URL with the current path and query', async () => {
    window.history.replaceState({}, '', '/setup?x=1')
    renderContinue()

    fireEvent.click(await screen.findByRole('button', { name: 'Open in browser' }))

    await vi.waitFor(() => {
      expect(mockOpenExternalUrl).toHaveBeenCalledExactlyOnceWith('https://hub.example/setup?x=1')
    })
  })

  it('reads the system info and the user again on a check', async () => {
    renderContinue()

    fireEvent.click(await screen.findByRole('button', { name: 'Check again' }))

    await vi.waitFor(() => {
      expect(mockLoadSystemInfo).toHaveBeenCalledExactlyOnceWith(true)
      expect(mockRefreshUser).toHaveBeenCalledTimes(1)
    })
  })

  it('renders the Hub line with the connected origin', async () => {
    renderContinue()

    expect(await screen.findByText('Hub: https://hub.example')).toBeInTheDocument()
  })
})
