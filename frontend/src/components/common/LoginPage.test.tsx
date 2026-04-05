import { MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
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
const mockLoadOAuthProviders = vi.fn(() => Promise.resolve([] as Record<string, unknown>[]))
vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  isSetupRequired: () => false,
  loadSystemInfo: () => Promise.resolve(),
  isSignupEnabled: () => mockIsSignupEnabled(),
  loadOAuthProviders: () => mockLoadOAuthProviders(),
}))

const mockLogin = vi.fn()
const mockUser = vi.fn<() => { username: string } | null>(() => null)
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser(),
    loading: () => false,
    error: () => null,
    login: mockLogin,
    logout: vi.fn(),
    setAuth: vi.fn(),
    isAuthenticated: () => false,
  }),
  AuthProvider: (props: { children: unknown }) => <>{props.children}</>,
}))

function renderLoginPage() {
  return render(() => (
    <MemoryRouter>
      <Route path="/" component={LoginPage} />
    </MemoryRouter>
  ))
}

describe('loginPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockIsSignupEnabled.mockReturnValue(false)
    mockLoadOAuthProviders.mockResolvedValue([])
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

  it('keeps button disabled after successful login', async () => {
    mockLogin.mockImplementation(() => {
      mockUser.mockReturnValue({ username: 'alice' })
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
})
