import { MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { SignupPage } from './SignupPage'

const mockGetSystemInfo = vi.fn()
const mockGetOAuthProviders = vi.fn()
vi.mock('~/api/clients', () => ({
  authClient: {
    getSystemInfo: (...args: unknown[]) => mockGetSystemInfo(...args),
    getOAuthProviders: (...args: unknown[]) => mockGetOAuthProviders(...args),
    signUp: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser: vi.fn(),
  },
}))

vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  loadSystemInfo: () => Promise.resolve(),
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
  AuthProvider: (props: { children: unknown }) => props.children,
}))

function renderSignupPage() {
  return render(() => (
    <MemoryRouter>
      <Route path="/" component={SignupPage} />
    </MemoryRouter>
  ))
}

describe('signupPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockGetSystemInfo.mockResolvedValue({ signupEnabled: true, soloMode: false })
    mockGetOAuthProviders.mockResolvedValue({ providers: [] })
  })

  it('renders password form when signup enabled and no oauth providers', async () => {
    renderSignupPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })
    expect(screen.getByLabelText('Password')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sign up' })).toBeInTheDocument()
    expect(screen.queryByText(/Sign up with/)).not.toBeInTheDocument()
  })

  it('renders oauth buttons with password form when providers configured', async () => {
    mockGetOAuthProviders.mockResolvedValue({
      providers: [
        { id: 'p1', name: 'Google', providerType: 'oidc', loginUrl: '/auth/oauth/p1/login' },
      ],
    })

    renderSignupPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign up with Google/)).toBeInTheDocument()
    })
    expect(screen.getByText(/or create an account with email/)).toBeInTheDocument()
    expect(screen.getByLabelText('Username')).toBeInTheDocument()
  })

  it('shows disabled message when signup disabled and no oauth', async () => {
    mockGetSystemInfo.mockResolvedValue({ signupEnabled: false, soloMode: false })

    renderSignupPage()

    await vi.waitFor(() => {
      expect(screen.getByText(/Sign Up Disabled/i)).toBeInTheDocument()
    })
  })
})
