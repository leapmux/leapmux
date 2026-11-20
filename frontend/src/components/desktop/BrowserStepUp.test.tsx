import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { browserAuthDriver, resetBrowserAuthDriver } from '~/test-support/browserAuthDriver'
import { BrowserStepUp } from './BrowserStepUp'

vi.mock('~/api/platformBridge', async () => {
  const { browserAuthPlatformMock } = await import('~/test-support/browserAuthDriver')
  return browserAuthPlatformMock()
})

const mockReloadUser = vi.hoisted(() => vi.fn<() => Promise<boolean>>())
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    loading: () => false,
    reloadUser: () => mockReloadUser(),
  }),
}))

const copyTextToClipboard = vi.hoisted(() => vi.fn<(text: string) => Promise<boolean>>())
vi.mock('~/lib/clipboard', () => ({
  copyTextToClipboard: (text: string) => copyTextToClipboard(text),
}))

function renderStepUp() {
  const onElevated = vi.fn()
  const result = render(() => <BrowserStepUp onElevated={onElevated} />)
  return { ...result, onElevated }
}

async function startCeremony() {
  fireEvent.click(await screen.findByRole('button', { name: 'Verify in your browser' }))
  await vi.waitFor(() => expect(browserAuthDriver.openExternalUrl).toHaveBeenCalled())
}

describe('BrowserStepUp', () => {
  beforeEach(() => {
    resetBrowserAuthDriver()
    copyTextToClipboard.mockReset()
    copyTextToClipboard.mockResolvedValue(true)
    mockReloadUser.mockReset()
    mockReloadUser.mockResolvedValue(true)
  })

  it('shows the reason and keeps the button on a failure', async () => {
    const { onElevated } = renderStepUp()
    await startCeremony()

    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: false, failure: 'the code expired' })

    expect(await screen.findByText('the code expired')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Verify in your browser' })).toBeInTheDocument()
    expect(onElevated).not.toHaveBeenCalled()
    expect(mockReloadUser).not.toHaveBeenCalled()
  })

  it('unlocks only after the verified session is reloaded', async () => {
    const { onElevated } = renderStepUp()
    await startCeremony()

    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    await vi.waitFor(() => expect(mockReloadUser).toHaveBeenCalledTimes(1))
    expect(onElevated).toHaveBeenCalledTimes(1)
  })

  it('keeps a usable retry when the verified session cannot be reloaded', async () => {
    mockReloadUser.mockResolvedValue(false)
    const { onElevated } = renderStepUp()
    await startCeremony()

    browserAuthDriver.emit({ ceremonyId: 'c-1', ok: true, failure: '' })

    expect(await screen.findByText(/could not read the verified session/i)).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Verify in your browser' })).toBeInTheDocument()
    expect(onElevated).not.toHaveBeenCalled()
  })

  it('opens the same URL again and starts no second ceremony', async () => {
    renderStepUp()
    await startCeremony()

    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()

    fireEvent.click(await screen.findByRole('button', { name: 'Open again' }))

    await vi.waitFor(() => expect(browserAuthDriver.openExternalUrl).toHaveBeenCalledTimes(2))
    expect(browserAuthDriver.openExternalUrl).toHaveBeenNthCalledWith(2, 'https://hub.example/authorize')
    expect(browserAuthDriver.startBrowserAuth).toHaveBeenCalledTimes(1)
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
