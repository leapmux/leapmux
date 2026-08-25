import { MemoryRouter, Route } from '@solidjs/router'
import { render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_BROWSER_PREFS, localStorageClearForTests, localStorageGet, resetStorageAccountForTests, setStorageAccount } from '~/lib/browserStorage'
import { applyTheme, DEFAULT_THEME_VALUE } from '~/lib/themeStore'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
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
  isEmailEnabled: () => false,
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

describe('the first-run setup page (SetupPage)', () => {
  it('welcomes the first administrator', async () => {
    renderSetup()
    expect(await screen.findByText('Welcome to LeapMux')).toBeInTheDocument()
  })

  // No chooser here, and not merely because nobody put one on this page: a
  // theme is stored per ACCOUNT, and this route exists precisely because no
  // account exists yet. There is nothing a picker could write to.
  it('offers no theme picker', async () => {
    renderSetup()
    await screen.findByText('Welcome to LeapMux')
    expect(screen.queryByTestId('theme-chooser')).toBeNull()
    expect(screen.queryByRole('radiogroup', { name: 'Theme mode' })).toBeNull()
  })

  it('paints the default palette, following the OS polarity', async () => {
    renderSetup()
    await screen.findByText('Welcome to LeapMux')
    expect(document.documentElement.getAttribute('data-ui-theme')).toBe('default')
  })

  // With NO ACCOUNT SET, which is the state this route actually renders in:
  // it exists because no account exists yet. Every account-scoped read and
  // write throws there, so a component that touched storage would take the
  // render down rather than merely read the wrong thing.
  //
  // The suite signs itself in (see `vitest.setup.ts`), so the account has to be
  // dropped here. Asserting "no key was written" under the suite's account
  // would pass for free -- `beforeEach` clears the store -- and would still
  // pass if this page read storage on every render.
  it('renders with no account set, so it touches no account-scoped key', async () => {
    resetStorageAccountForTests()
    try {
      renderSetup()
      await screen.findByText('Welcome to LeapMux')
      expect(document.documentElement.getAttribute('data-ui-theme')).toBe('default')
    }
    finally {
      setStorageAccount(TEST_USER_ID)
    }
    expect(localStorageGet(KEY_BROWSER_PREFS)).toBeUndefined()
  })
})
