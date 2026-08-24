import { render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_BROWSER_PREFS, localStorageClearForTests, localStorageGet, resetStorageAccountForTests, setStorageAccount } from '~/lib/browserStorage'
import { applyTheme, DEFAULT_THEME_VALUE } from '~/lib/themeStore'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
import { LauncherView } from './LauncherView'

const bridgeMocks = vi.hoisted(() => ({
  getRuntimeState: vi.fn(),
  getStartupInfo: vi.fn(),
  checkFullDiskAccess: vi.fn(),
  restoreWindowGeometry: vi.fn(),
}))

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    getRuntimeState: bridgeMocks.getRuntimeState,
    restoreWindowGeometry: bridgeMocks.restoreWindowGeometry,
    platformBridge: {
      ...actual.platformBridge,
      getStartupInfo: bridgeMocks.getStartupInfo,
      checkFullDiskAccess: bridgeMocks.checkFullDiskAccess,
    },
  }
})

describe('launcherView', () => {
  beforeEach(() => {
    window.history.replaceState({}, '', '/?cleanup_error=lease%20release%20failed')
    bridgeMocks.getRuntimeState.mockResolvedValue({ shellMode: 'launcher', connected: false })
    bridgeMocks.getStartupInfo.mockResolvedValue({
      config: { mode: '', hub_url: '', window_width: 0, window_height: 0, window_mode: 'normal' },
      buildInfo: { version: '', commitHash: '', commitTime: '', buildTime: '', branch: '' },
    })
    bridgeMocks.checkFullDiskAccess.mockResolvedValue(true)
    bridgeMocks.restoreWindowGeometry.mockResolvedValue(undefined)
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
      setStorageAccount(TEST_USER_ID)
    }
    expect(localStorageGet(KEY_BROWSER_PREFS)).toBeUndefined()
  })
})
