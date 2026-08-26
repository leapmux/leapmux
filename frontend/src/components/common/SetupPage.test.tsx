import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_BROWSER_PREFS, localStorageClearForTests, localStorageGet, resetStorageAccountForTests, setStorageAccount } from '~/lib/browserStorage'
import { applyTheme, DEFAULT_THEME_VALUE } from '~/lib/themeStore'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
import { withPreferences } from '~/test-support/preferencesProvider'
import { resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'
import { SetupPage } from './SetupPage'

const mockSignUp = vi.fn<(...args: unknown[]) => Promise<unknown>>()
const mockBeginPasskeySignUp = vi.fn<(...args: unknown[]) => Promise<unknown>>()
const mockFinishPasskeySignUp = vi.fn<(...args: unknown[]) => Promise<unknown>>()

vi.mock('~/api/clients', () => ({
  authClient: {
    signUp: (...args: unknown[]) => mockSignUp(...args),
    beginPasskeySignUp: (...args: unknown[]) => mockBeginPasskeySignUp(...args),
    finishPasskeySignUp: (...args: unknown[]) => mockFinishPasskeySignUp(...args),
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

// Only the browser ceremony is faked; passkeyErrorMessage stays real, so the
// cancel-vs-failure rule under test is the one the component ships with.
vi.mock('~/lib/webauthn', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/webauthn')>(),
  startRegistration: vi.fn().mockResolvedValue('{"id":"cred"}'),
}))

// The shared mock, so this page reads the same getters (passkeyBlocker,
// isCaptchaEnabled) through the same reactive snapshot every other auth
// surface does.
vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

const mockSetAuth = vi.fn()

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => null,
    loading: () => false,
    error: () => null,
    login: vi.fn(),
    logout: vi.fn(),
    setAuth: mockSetAuth,
    isAuthenticated: () => false,
  }),
  AuthProvider: (props: { children: unknown }) => <>{props.children}</>,
}))

function renderSetup() {
  const history = createMemoryHistory()
  const navigations: string[] = []
  history.listen(value => navigations.push(value))
  const result = render(() => (
    <MemoryRouter history={history}>
      <Route path="/" component={withPreferences(() => <SetupPage />)} />
      <Route path="/home" component={() => <div data-testid="app-home" />} />
    </MemoryRouter>
  ))
  return { ...result, navigations }
}

/** Fill the fields every sign-up method needs. */
function fillCommonFields(username: string) {
  fireEvent.input(screen.getByLabelText('Username'), { target: { value: username } })
  fireEvent.input(screen.getByLabelText('Email'), { target: { value: `${username}@example.com` } })
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorageClearForTests()
  resetSystemInfoMock()
  applyTheme(DEFAULT_THEME_VALUE)
  setSystemInfoMock({ setupRequired: true })
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

  // The page carries NO first-run check of its own any more. It used to read
  // isSetupRequired() from onMount, before the system info had arrived, so a
  // cold load answered the fabricated `false` and bounced a direct visit to
  // /login and back. SetupGate owns both directions now, above the router
  // outlet -- which is why the page renders here although the snapshot says
  // the hub is already set up.
  it('renders whatever the setup state says, leaving that decision to SetupGate', async () => {
    setSystemInfoMock({ setupRequired: false })
    const { navigations } = renderSetup()
    expect(await screen.findByText('Welcome to LeapMux')).toBeInTheDocument()
    expect(navigations).toEqual([])
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

  // The first administrator picks a credential the same way anybody else
  // does. The hub used to refuse a passkey during initial setup, so this page
  // hid the pills; both halves went at once.
  it('offers both sign-up methods', async () => {
    renderSetup()
    await screen.findByText('Welcome to LeapMux')
    expect(screen.getByRole('radio', { name: 'Password' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Passkey' })).toBeInTheDocument()
  })

  // `admin` is squat-protected in public signup and claimable here, so the
  // passkey path has to accept it too -- the reserved rule is shared, and it
  // took the setup exemption on the password path only.
  it('registers the first administrator with a passkey', async () => {
    mockBeginPasskeySignUp.mockResolvedValue({ sessionId: 's-1', optionsJson: '{}' })
    mockFinishPasskeySignUp.mockResolvedValue({ user: { id: 'u-1', username: 'admin' } })

    renderSetup()
    await screen.findByText('Welcome to LeapMux')
    fillCommonFields('admin')
    fireEvent.click(screen.getByRole('radio', { name: 'Passkey' }))
    fireEvent.click(screen.getByRole('button', { name: 'Sign up with passkey' }))

    await vi.waitFor(() => {
      expect(mockFinishPasskeySignUp).toHaveBeenCalledWith({ sessionId: 's-1', credentialJson: '{"id":"cred"}' })
    })
    expect(mockBeginPasskeySignUp).toHaveBeenCalledWith(
      expect.objectContaining({ username: 'admin', email: 'admin@example.com' }),
    )
    // The RPC creates the session, so the page adopts it and leaves.
    expect(mockSetAuth).toHaveBeenCalledWith({ id: 'u-1', username: 'admin' })
    expect(mockSignUp).not.toHaveBeenCalled()
  })

  it('still registers the first administrator with a password', async () => {
    mockSignUp.mockResolvedValue({ user: { id: 'u-1', username: 'admin' } })

    renderSetup()
    await screen.findByText('Welcome to LeapMux')
    fillCommonFields('admin')
    fireEvent.input(screen.getByLabelText('New Password'), { target: { value: 'strongpass1' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'strongpass1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create account' }))

    await vi.waitFor(() => {
      expect(mockSetAuth).toHaveBeenCalledWith({ id: 'u-1', username: 'admin' })
    })
    expect(mockBeginPasskeySignUp).not.toHaveBeenCalled()
  })
})
