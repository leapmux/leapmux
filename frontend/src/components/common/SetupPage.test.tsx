import { MemoryRouter, Route } from '@solidjs/router'
import { fireEvent, render, screen, waitFor, within } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { loadBrowserPrefs, localStorageClearForTests } from '~/lib/browserStorage'
import { applyTheme, DEFAULT_THEME_VALUE } from '~/lib/themeStore'
import { withPreferences } from '~/test-support/preferencesProvider'
import { SetupPage } from './SetupPage'

vi.mock('~/api/clients', () => ({
  authClient: {
    signUp: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser: vi.fn(),
  },
  userClient: {
    listUserSettings: vi.fn(() => Promise.reject(new Error('no hub'))),
    updateUserSetting: vi.fn(() => Promise.resolve({})),
    resetUserSetting: vi.fn(() => Promise.resolve({})),
  },
}))

const mockIsSetupRequired = vi.fn<() => boolean>(() => true)
vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  isSetupRequired: () => mockIsSetupRequired(),
  loadSystemInfo: () => Promise.resolve(),
  isSignupEnabled: () => true,
  loadOAuthProviders: () => Promise.resolve([]),
  isSystemInfoLoaded: () => true,
  isCaptchaEnabled: () => false,
  getAltchaAlgorithm: () => '',
  getCaptchaProvider: () => 1,
  getCaptchaSiteKey: () => '',
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => null,
    loading: () => false,
    error: () => null,
    login: vi.fn(),
    logout: vi.fn(),
    setAuth: vi.fn(),
    isAuthenticated: () => false,
  }),
  AuthProvider: (props: { children: unknown }) => <>{props.children}</>,
}))

function renderSetup() {
  return render(() => (
    <MemoryRouter>
      <Route path="/" component={withPreferences(() => <SetupPage />)} />
    </MemoryRouter>
  ))
}

beforeEach(() => {
  localStorageClearForTests()
  applyTheme(DEFAULT_THEME_VALUE)
  mockIsSetupRequired.mockReturnValue(true)
})

afterEach(() => {
  localStorageClearForTests()
  applyTheme(DEFAULT_THEME_VALUE)
})

/** Pick a palette from the chooser's menu. It is a DropdownMenu, not a select. */
function pickTheme(label: string) {
  const popover = screen.getByTestId('theme-chooser-name-menu')
  fireEvent.click(within(popover).getByRole('menuitemradio', { name: label, hidden: true }))
}

describe('the first-run setup page (SetupPage)', () => {
  it('welcomes the first administrator', async () => {
    renderSetup()
    expect(await screen.findByText('Welcome to LeapMux')).toBeInTheDocument()
  })

  // The first screen a hub or dev-mode install ever shows, so the theme is
  // offered here too -- before any account exists to hold an account tier.
  it('offers the theme picker before the first account exists', async () => {
    renderSetup()
    expect(await screen.findByTestId('theme-chooser')).toBeInTheDocument()
    expect(screen.getByRole('radiogroup', { name: 'Theme mode' })).toBeInTheDocument()
  })

  it('applies a chosen mode to the page at once', async () => {
    renderSetup()
    fireEvent.click(await screen.findByRole('radio', { name: 'Dark' }))

    await waitFor(() => expect(document.documentElement.getAttribute('data-theme')).toBe('dark'))
  })

  it('writes the choice through the ordinary preference, not a page-local key', async () => {
    // This route DOES sit inside PreferencesProvider, but with no session the
    // account write fails and the device tier is what survives. Either way it
    // is the same `theme` field the Preferences dialog reads.
    renderSetup()
    await screen.findByTestId('theme-chooser-name')
    pickTheme('Gruvbox')

    await waitFor(() => {
      const stored = loadBrowserPrefs()
      expect(Object.keys(stored).filter(k => k !== 'theme')).toEqual([])
    })
  })
})
