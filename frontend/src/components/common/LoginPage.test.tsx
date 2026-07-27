import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { LoginPage } from './LoginPage'

vi.mock('~/api/clients', () => ({
  authClient: {
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser: vi.fn(),
  },
}))

const mockIsSignupEnabled = vi.fn<() => boolean>(() => false)
const mockIsSoloMode = vi.fn<() => boolean>(() => false)
const mockIsSetupRequired = vi.fn<() => boolean>(() => false)
const mockLoadOAuthProviders = vi.fn(() => Promise.resolve([] as Record<string, unknown>[]))
vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => mockIsSoloMode(),
  isSetupRequired: () => mockIsSetupRequired(),
  loadSystemInfo: () => Promise.resolve(),
  isSignupEnabled: () => mockIsSignupEnabled(),
  loadOAuthProviders: () => mockLoadOAuthProviders(),
}))

const mockLogin = vi.fn()
const mockUser = vi.fn<() => { id: string, username: string } | null>(() => null)
// `loading` is a real signal so a test can hold the page in the pre-bootstrap
// state and then release it, which is the whole shape of the race below.
const [authLoading, setAuthLoading] = createSignal(false)
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    loading: () => authLoading(),
    error: () => null,
    login: mockLogin,
    logout: vi.fn(),
    setAuth: vi.fn(),
    isAuthenticated: () => false,
  }),
  AuthProvider: (props: { children: unknown }) => <>{props.children}</>,
}))

function renderLoginPage(initialPath = '/login') {
  const history = createMemoryHistory()
  history.set({ value: initialPath, replace: true, scroll: false })
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/login" component={LoginPage} />
      <Route path="/" component={() => <div data-testid="app-home" />} />
      <Route path="/setup" component={() => <div data-testid="setup-page" />} />
    </MemoryRouter>
  ))
}

describe('loginPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockIsSignupEnabled.mockReturnValue(false)
    mockIsSoloMode.mockReturnValue(false)
    mockIsSetupRequired.mockReturnValue(false)
    mockLoadOAuthProviders.mockResolvedValue([])
    setAuthLoading(false)
  })

  // These three pin the bootstrap race. The system-info getters are plain
  // module reads whose pre-fetch values are fabrications (soloMode = false,
  // setupRequired = false), so sampling them before bootstrap resolves is
  // sampling a guess. This used to run in onMount, which fires once and never
  // re-checks: on any load that beat the RPC, a solo-mode visitor was stranded
  // on a credential form that cannot succeed, and the signup link was pinned
  // off. The redirects must therefore wait for auth.loading() to clear and
  // must still fire afterwards.
  // Each of these models the getter faithfully: it answers the module's
  // fabricated default while bootstrap is in flight, and the truth afterwards.
  // Reading it early therefore yields the WRONG answer, not merely an early
  // one -- which is what made the onMount version fail silently and forever.
  it('waits for bootstrap before deciding, then redirects a solo hub', async () => {
    let soloNow = false
    mockIsSoloMode.mockImplementation(() => soloNow)
    setAuthLoading(true)

    renderLoginPage()

    // Bootstrap is still in flight, so nothing has been decided yet.
    expect(screen.queryByTestId('app-home')).not.toBeInTheDocument()

    soloNow = true
    setAuthLoading(false)

    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
  })

  it('redirects to setup once bootstrap reports a fresh install', async () => {
    let setupNow = false
    mockIsSetupRequired.mockImplementation(() => setupNow)
    setAuthLoading(true)

    renderLoginPage()
    expect(screen.queryByTestId('setup-page')).not.toBeInTheDocument()

    setupNow = true
    setAuthLoading(false)

    expect(await screen.findByTestId('setup-page')).toBeInTheDocument()
  })

  it('shows the signup link once bootstrap enables it', async () => {
    // NOT derived from the loading signal: the real isSignupEnabled() is a
    // plain module read with no reactivity, so a mock that peeked at a signal
    // would hand the component a dependency production does not have and the
    // test would pass against the broken version too.
    let signupEnabledNow = false
    mockIsSignupEnabled.mockImplementation(() => signupEnabledNow)
    setAuthLoading(true)

    renderLoginPage()
    expect(screen.queryByRole('link', { name: 'Sign up' })).not.toBeInTheDocument()

    signupEnabledNow = true
    setAuthLoading(false)

    await vi.waitFor(() => {
      expect(screen.getByRole('link', { name: 'Sign up' })).toBeInTheDocument()
    })
  })

  it('renders email/password form when no oauth providers', async () => {
    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })
    expect(screen.getByLabelText('Password')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(screen.queryByText(/Sign in with/)).not.toBeInTheDocument()
  })

  it('renders oauth buttons when providers are configured', async () => {
    mockLoadOAuthProviders.mockResolvedValue([
      { id: 'p1', name: 'Google', providerType: 'oidc', loginUrl: '/auth/oauth/p1/login' },
      { id: 'p2', name: 'GitHub', providerType: 'github', loginUrl: '/auth/oauth/p2/login' },
    ])

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign in with Google/)).toBeInTheDocument()
    })
    expect(screen.getByText(/Sign in with GitHub/)).toBeInTheDocument()
    expect(screen.getByText('or')).toBeInTheDocument()
    expect(screen.getByLabelText('Username')).toBeInTheDocument()
  })

  it('oauth button links to correct login url', async () => {
    mockLoadOAuthProviders.mockResolvedValue([
      { id: 'p1', name: 'TestProvider', providerType: 'oidc', loginUrl: '/auth/oauth/p1/login' },
    ])

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign in with TestProvider/)).toBeInTheDocument()
    })

    const link = screen.getByText(/Sign in with TestProvider/).closest('a')
    expect(link).toHaveAttribute('href', '/auth/oauth/p1/login')
  })

  it('shows signup link when signup is enabled', async () => {
    mockIsSignupEnabled.mockReturnValue(true)

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText('Sign up')).toBeInTheDocument()
    })
  })

  it('renders provider with long name correctly', async () => {
    mockLoadOAuthProviders.mockResolvedValue([
      { id: 'p1', name: 'Corporate Azure Active Directory', providerType: 'oidc', loginUrl: '/auth/oauth/p1/login' },
    ])

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign in with Corporate Azure Active Directory/)).toBeInTheDocument()
    })
  })

  it('disables Sign in button when username is empty', async () => {
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()
  })

  it('disables Sign in button when password is empty', async () => {
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()
  })

  it('disables Sign in button when both fields are empty', async () => {
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    })

    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()
  })

  it('enables Sign in button when both fields have values', async () => {
    renderLoginPage()
    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled()
  })

  it('keeps button disabled after successful login', async () => {
    mockLogin.mockImplementation(() => {
      mockUser.mockReturnValue({ id: 'u1', username: 'alice' })
      return Promise.resolve()
    })

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })

    const button = screen.getByRole('button', { name: 'Sign in' })
    fireEvent.click(button)

    // Wait for login to complete.
    await vi.waitFor(() => {
      expect(mockLogin).toHaveBeenCalledOnce()
    })

    // Flush microtasks so the async handler finishes.
    await new Promise(r => setTimeout(r, 0))

    // The button should never revert to 'Sign in' after a successful login.
    expect(screen.queryByRole('button', { name: 'Sign in' })).not.toBeInTheDocument()
  })

  it('redirects to / after successful login when no redirect param', async () => {
    mockLogin.mockImplementation(() => {
      mockUser.mockReturnValue({ id: 'u1', username: 'alice' })
      return Promise.resolve()
    })

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    fireEvent.input(screen.getByLabelText('Username'), { target: { value: 'alice' } })
    fireEvent.input(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))

    // Flat user-owned home is `/` (no org slug).
    expect(await screen.findByTestId('app-home')).toBeInTheDocument()
  })
})
