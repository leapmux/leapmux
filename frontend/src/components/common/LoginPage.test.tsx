import { MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { LoginPage } from './LoginPage'

// Mock the auth client before importing the component.
const mockGetSystemInfo = vi.fn()
const mockGetOAuthProviders = vi.fn()
vi.mock('~/api/clients', () => ({
  authClient: {
    getSystemInfo: (...args: unknown[]) => mockGetSystemInfo(...args),
    getOAuthProviders: (...args: unknown[]) => mockGetOAuthProviders(...args),
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser: vi.fn(),
  },
}))

vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  loadSystemInfo: () => Promise.resolve(),
}))

const mockLogin = vi.fn()
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => null,
    loading: () => false,
    error: () => null,
    login: mockLogin,
    logout: vi.fn(),
    setAuth: vi.fn(),
    isAuthenticated: () => false,
  }),
  AuthProvider: (props: { children: unknown }) => props.children,
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
    mockGetSystemInfo.mockResolvedValue({ signupEnabled: false, soloMode: false })
    mockGetOAuthProviders.mockResolvedValue({ providers: [] })
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
    mockGetOAuthProviders.mockResolvedValue({
      providers: [
        { id: 'p1', name: 'Google', providerType: 'oidc', loginUrl: '/auth/oauth/p1/login' },
        { id: 'p2', name: 'GitHub', providerType: 'github', loginUrl: '/auth/oauth/p2/login' },
      ],
    })

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign in with Google/)).toBeInTheDocument()
    })
    expect(screen.getByText(/Sign in with GitHub/)).toBeInTheDocument()
    expect(screen.getByText('or')).toBeInTheDocument()
    expect(screen.getByLabelText('Username')).toBeInTheDocument()
  })

  it('oauth button links to correct login url', async () => {
    mockGetOAuthProviders.mockResolvedValue({
      providers: [
        { id: 'p1', name: 'TestProvider', providerType: 'oidc', loginUrl: '/auth/oauth/p1/login' },
      ],
    })

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign in with TestProvider/)).toBeInTheDocument()
    })

    const link = screen.getByText(/Sign in with TestProvider/).closest('a')
    expect(link).toHaveAttribute('href', '/auth/oauth/p1/login')
  })

  it('shows signup link when signup is enabled', async () => {
    mockGetSystemInfo.mockResolvedValue({ signupEnabled: true, soloMode: false })

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText('Sign up')).toBeInTheDocument()
    })
  })

  it('renders provider with long name correctly', async () => {
    mockGetOAuthProviders.mockResolvedValue({
      providers: [
        { id: 'p1', name: 'Corporate Azure Active Directory', providerType: 'oidc', loginUrl: '/auth/oauth/p1/login' },
      ],
    })

    renderLoginPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign in with Corporate Azure Active Directory/)).toBeInTheDocument()
    })
  })
})
