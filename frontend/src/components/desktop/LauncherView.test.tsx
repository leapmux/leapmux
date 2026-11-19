import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_BROWSER_PREFS, localStorageClearForTests, localStorageGet, resetStorageAccountForTests, setStorageAccountForTests } from '~/lib/browserStorage'
import { applyTheme, DEFAULT_THEME_VALUE } from '~/lib/themeStore'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
import { LauncherView } from './LauncherView'

const bridgeMocks = vi.hoisted(() => ({
  getRuntimeState: vi.fn(),
  getStartupInfo: vi.fn(),
  checkFullDiskAccess: vi.fn(),
  restoreWindowGeometry: vi.fn(),
  connectDistributed: vi.fn(),
}))

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    desktopHub: () => null,
    getRuntimeState: bridgeMocks.getRuntimeState,
    restoreWindowGeometry: bridgeMocks.restoreWindowGeometry,
    platformBridge: {
      ...actual.platformBridge,
      getStartupInfo: bridgeMocks.getStartupInfo,
      checkFullDiskAccess: bridgeMocks.checkFullDiskAccess,
      connectDistributed: bridgeMocks.connectDistributed,
    },
  }
})

describe('LauncherView', () => {
  beforeEach(() => {
    window.history.replaceState({}, '', '/?cleanup_error=lease%20release%20failed')
    bridgeMocks.getRuntimeState.mockResolvedValue({ shellMode: 'launcher', connected: false })
    bridgeMocks.getStartupInfo.mockResolvedValue({
      config: {
        mode: '',
        hub_url: '',
        window_width: 0,
        window_height: 0,
        window_mode: 'normal',
      },
      buildInfo: {
        version: '',
        commitHash: '',
        commitTime: '',
        buildTime: '',
        branch: '',
      },
    })
    bridgeMocks.checkFullDiskAccess.mockResolvedValue(true)
    bridgeMocks.restoreWindowGeometry.mockResolvedValue(undefined)
    bridgeMocks.connectDistributed.mockReset()
    bridgeMocks.connectDistributed.mockResolvedValue(undefined)
    localStorageClearForTests()
    applyTheme(DEFAULT_THEME_VALUE)
  })

  afterEach(() => {
    localStorageClearForTests()
    applyTheme(DEFAULT_THEME_VALUE)
  })

  it('surfaces a cleanup warning carried across committed navigation', async () => {
    render(() => <LauncherView onConnected={() => {}} />)
    expect(await screen.findByText('lease release failed')).toBeInTheDocument()
  })

  // This view renders OUTSIDE every provider (see app.tsx), and it is the only
  // surface in the app that does. That is exactly why it carries no theme
  // control: every stored preference is scoped to an account, and the launcher
  // exists before there is a hub connection, let alone an account.
  it('offers no theme picker', async () => {
    render(() => <LauncherView onConnected={() => {}} />)
    await screen.findByText('lease release failed')
    expect(screen.queryByTestId('theme-chooser')).toBeNull()
  })

  // The load-bearing half of the pair: with no provider above it, the palette
  // still has to be right, and `~/lib/themeStore` is what answers.
  it('paints the default palette with no provider above it', async () => {
    render(() => <LauncherView onConnected={() => {}} />)
    await screen.findByText('lease release failed')
    expect(document.documentElement.getAttribute('data-ui-theme')).toBe('default')
  })

  // With NO ACCOUNT SET, which is the state the launcher actually renders in:
  // it is the screen shown before there is a hub connection, let alone an
  // identity. Every account-scoped read and write throws there, so a component
  // that touched storage would take the render down.
  //
  // The suite signs itself in (see `vitest.setup.ts`), so the account has to be
  // dropped here. Asserting "no key was written" under the suite's account
  // would pass for free -- `beforeEach` clears the store -- and would still
  // pass if the launcher read storage on every render.
  it('renders with no account set, so it touches no account-scoped key', async () => {
    resetStorageAccountForTests()
    try {
      render(() => <LauncherView onConnected={() => {}} />)
      await screen.findByText('lease release failed')
      expect(document.documentElement.getAttribute('data-ui-theme')).toBe('default')
    }
    finally {
      setStorageAccountForTests(TEST_USER_ID)
    }
    expect(localStorageGet(KEY_BROWSER_PREFS)).toBeUndefined()
  })

  // The shell decides the launch state, because only it knows both the launch
  // arguments and the cached tray setting, and it knows them before this view
  // exists. This is the one seam that carries the decision from
  // `getStartupInfo` into `restoreWindowGeometry`, and a one-sided edit to
  // either end would leave a login launch silently opening a window.
  it('forwards the shell launch decision into the geometry restore', async () => {
    bridgeMocks.getStartupInfo.mockResolvedValue({
      config: {
        mode: '',
        hub_url: '',
        window_width: 1280,
        window_height: 800,
        window_mode: 'maximized',
      },
      buildInfo: {
        version: '',
        commitHash: '',
        commitTime: '',
        buildTime: '',
        branch: '',
      },
      launchVisibility: 'hidden',
    })

    render(() => <LauncherView onConnected={() => {}} />)
    await screen.findByText('lease release failed')

    await vi.waitFor(() => {
      expect(bridgeMocks.restoreWindowGeometry).toHaveBeenCalledWith(1280, 800, 'maximized', 'hidden')
    })
  })

  /** Selects the distributed mode card and types a Hub URL. */
  async function chooseDistributed(hubUrl: string) {
    fireEvent.click(await screen.findByRole('button', { name: /Distributed/ }))
    fireEvent.input(await screen.findByLabelText('Hub URL'), {
      target: { value: hubUrl },
    })
  }

  // E3: the distributed branch runs its connect call and then hands control
  // over, exactly as the solo branch does -- it does not wait for the shell to
  // replace the page.
  it('runs onConnected once after a distributed connect succeeds', async () => {
    const onConnected = vi.fn()
    render(() => <LauncherView onConnected={onConnected} />)
    await screen.findByText('lease release failed')

    await chooseDistributed('https://hub.example')
    fireEvent.click(await screen.findByRole('button', { name: 'Connect' }))

    await vi.waitFor(() => {
      expect(onConnected).toHaveBeenCalledTimes(1)
    })
    expect(bridgeMocks.connectDistributed).toHaveBeenCalledExactlyOnceWith('https://hub.example')
  })

  // A failure restores the launcher and reports why, and no connection is
  // announced.
  it('shows the message when a distributed connect fails', async () => {
    const onConnected = vi.fn()
    bridgeMocks.connectDistributed.mockReset()
    bridgeMocks.connectDistributed.mockRejectedValue(new Error('no such hub'))
    render(() => <LauncherView onConnected={onConnected} />)
    await screen.findByText('lease release failed')

    await chooseDistributed('https://hub.example')
    fireEvent.click(await screen.findByRole('button', { name: 'Connect' }))

    expect(await screen.findByText('no such hub')).toBeInTheDocument()
    expect(onConnected).not.toHaveBeenCalled()
    expect(bridgeMocks.connectDistributed).toHaveBeenCalledExactlyOnceWith('https://hub.example')
  })
})
