import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { loadBrowserPrefs, localStorageClearForTests } from '~/lib/browserStorage'
import { applyTheme, DEFAULT_THEME_VALUE } from '~/lib/themeStore'
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

/** Pick a palette from the chooser's menu. It is a DropdownMenu, not a select. */
function pickTheme(label: string) {
  const popover = screen.getByTestId('theme-chooser-name-menu')
  fireEvent.click(within(popover).getByRole('menuitemradio', { name: label, hidden: true }))
}

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

  // This view renders OUTSIDE every provider (see app.tsx), so the chooser has
  // no account tier here. It must still write the device tier of the ordinary
  // `theme` preference, so the choice is the one the Preferences dialog reports
  // once the app connects.
  describe('theme picker', () => {
    it('offers the theme picker before the app has a session', () => {
      render(() => <LauncherView onConnected={() => {}} />)
      expect(screen.getByTestId('theme-chooser')).toBeInTheDocument()
      expect(screen.getByRole('radiogroup', { name: 'Theme mode' })).toBeInTheDocument()
    })

    it('persists a palette to the shared browser-prefs document', () => {
      render(() => <LauncherView onConnected={() => {}} />)
      pickTheme('Nord')

      expect(loadBrowserPrefs().theme).toEqual({ name: 'nord', mode: 'system' })
    })

    it('persists a mode without discarding the palette beside it', () => {
      render(() => <LauncherView onConnected={() => {}} />)
      pickTheme('Catppuccin')
      fireEvent.click(screen.getByRole('radio', { name: 'Dark' }))

      expect(loadBrowserPrefs().theme).toEqual({ name: 'catppuccin', mode: 'dark' })
    })

    it('paints the launcher itself with the chosen theme', () => {
      render(() => <LauncherView onConnected={() => {}} />)
      fireEvent.click(screen.getByRole('radio', { name: 'Dark' }))

      expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    })
  })
})
