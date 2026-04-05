/// <reference types="vitest/globals" />
import { render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { AuthProvider, useAuth } from './AuthContext'

const mockGetCurrentUser = vi.fn()
vi.mock('~/api/clients', () => ({
  authClient: {
    getCurrentUser: (...args: unknown[]) => mockGetCurrentUser(...args),
    getSystemInfo: vi.fn().mockResolvedValue({ signupEnabled: false, soloMode: false }),
    login: vi.fn(),
    logout: vi.fn(),
  },
}))

vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  loadSystemInfo: () => Promise.resolve(),
}))

function TestConsumer() {
  const auth = useAuth()
  return (
    <div>
      <span data-testid="authenticated">{auth.isAuthenticated() ? 'yes' : 'no'}</span>
      <span data-testid="username">{auth.user()?.username ?? 'none'}</span>
    </div>
  )
}

function renderWithAuth() {
  return render(() => (
    <AuthProvider>
      <TestConsumer />
    </AuthProvider>
  ))
}

describe('authContext', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('restores session from cookie on mount when getCurrentUser succeeds', async () => {
    mockGetCurrentUser.mockResolvedValue({
      user: { id: 'u1', username: 'testuser', orgId: 'o1', isAdmin: false },
    })

    renderWithAuth()

    await vi.waitFor(() => {
      expect(screen.getByTestId('authenticated')).toHaveTextContent('yes')
    })
    expect(screen.getByTestId('username')).toHaveTextContent('testuser')
  })

  it('stays unauthenticated when getCurrentUser fails (expired/no cookie)', async () => {
    mockGetCurrentUser.mockRejectedValue(new Error('unauthenticated'))

    renderWithAuth()

    await vi.waitFor(() => {
      expect(screen.getByTestId('authenticated')).toHaveTextContent('no')
    })
    expect(screen.getByTestId('username')).toHaveTextContent('none')
  })

  it('works after oauth callback redirect (page loads with cookie)', async () => {
    // Simulate: user just returned from OAuth callback with a valid session cookie.
    // AuthContext.onMount calls getCurrentUser, which succeeds because the cookie is set.
    mockGetCurrentUser.mockResolvedValue({
      user: { id: 'u2', username: 'oauth-user', orgId: 'o2', isAdmin: false, oauthProviders: ['Google'] },
    })

    renderWithAuth()

    await vi.waitFor(() => {
      expect(screen.getByTestId('authenticated')).toHaveTextContent('yes')
    })
    expect(screen.getByTestId('username')).toHaveTextContent('oauth-user')
  })
})
