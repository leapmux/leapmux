import type { AuthState } from './AuthContext'
/// <reference types="vitest/globals" />
import type { User } from '~/generated/leapmux/v1/auth_pb'
import { render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { AuthProvider, useAuth } from './AuthContext'

const mockGetCurrentUser = vi.fn()
const mockLogin = vi.fn()
vi.mock('~/api/clients', () => ({
  authClient: {
    getCurrentUser: (...args: unknown[]) => mockGetCurrentUser(...args),
    getSystemInfo: vi.fn().mockResolvedValue({ signupEnabled: false, soloMode: false }),
    login: (...args: unknown[]) => mockLogin(...args),
    logout: vi.fn().mockResolvedValue({}),
  },
}))

const mockCloseAll = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  channelManager: {
    closeAll: () => mockCloseAll(),
  },
}))

const mockResetTunnels = vi.fn<() => Promise<void>>()
vi.mock('~/api/platformBridge', () => ({
  // ~/api/transport imports these from the same module at load time, so the
  // factory must provide them alongside the bridge under test.
  desktopFetch: vi.fn(),
  getCapabilities: () => ({ hubTransport: 'direct' }),
  isTauriApp: () => false,
  platformBridge: {
    resetTunnels: () => mockResetTunnels(),
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

// Capture the live auth context so tests can drive login()/logout() directly.
function renderWithAuthCapture(): { auth: () => AuthState } {
  let captured: AuthState | undefined
  function Capture() {
    captured = useAuth()
    return null
  }
  render(() => (
    <AuthProvider>
      <Capture />
      <TestConsumer />
    </AuthProvider>
  ))
  return { auth: () => captured! }
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

  it('drops pooled channels and resets sidecar tunnels when logging in over a different user', async () => {
    // A still-authenticated user (a bookmarked /login, a stale tab) submits
    // another account's credentials. That is an identity transition just like
    // logout, so the previous user's pooled E2EE channels must be released
    // eagerly rather than left to the lazy per-request identity check -- and the
    // desktop sidecar's tunnels (which carry no identity of their own) must be
    // reset so they cannot keep relaying under the old user.
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', orgId: 'o1', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()
    mockResetTunnels.mockResolvedValue(undefined)

    mockLogin.mockResolvedValue({ user: { id: 'u2', username: 'bob', orgId: 'o2', isAdmin: false } })
    await auth().login('bob', 'pw')

    expect(mockCloseAll).toHaveBeenCalledOnce()
    expect(mockResetTunnels).toHaveBeenCalledOnce()
    expect(screen.getByTestId('username')).toHaveTextContent('bob')
  })

  it('does not drop pooled channels when re-authenticating as the same user', async () => {
    // Re-login as the SAME identity (a session refresh) is not a transition, so
    // the pooled channels -- already correct for this user -- must be kept.
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', orgId: 'o1', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()

    mockLogin.mockResolvedValue({ user: { id: 'u1', username: 'alice', orgId: 'o1', isAdmin: false } })
    await auth().login('alice', 'pw')

    expect(mockCloseAll).not.toHaveBeenCalled()
    expect(mockResetTunnels).not.toHaveBeenCalled()
  })

  it('drops pooled channels and resets sidecar tunnels on logout', async () => {
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', orgId: 'o1', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()
    mockResetTunnels.mockResolvedValue(undefined)

    await auth().logout()

    expect(mockCloseAll).toHaveBeenCalledOnce()
    expect(mockResetTunnels).toHaveBeenCalledOnce()
    expect(screen.getByTestId('authenticated')).toHaveTextContent('no')
  })

  it('drops pooled channels when any setUser path swaps to a different identity', async () => {
    // The release is driven off the `user` signal, so EVERY identity transition
    // covers by construction -- not just login/logout. setAuth is normally null->A
    // account creation, but a future impersonation seed or server-side session swap
    // that changes the id must release too; the old imperative wiring only released
    // at login/logout/auth-error and would have leaked the previous user's channels.
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', orgId: 'o1', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()
    mockResetTunnels.mockResolvedValue(undefined)

    auth().setAuth({ id: 'u2', username: 'bob', orgId: 'o2', isAdmin: false } as unknown as User)

    await vi.waitFor(() => expect(mockCloseAll).toHaveBeenCalledOnce())
    expect(mockResetTunnels).toHaveBeenCalledOnce()
    expect(screen.getByTestId('username')).toHaveTextContent('bob')
  })

  it('does not drop pooled channels on the initial session restore', async () => {
    // null -> first-user is not an identity SWAP: there is nothing pooled to release,
    // and a spurious resetTunnels must not fire on a fresh page load.
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', orgId: 'o1', isAdmin: false } })
    renderWithAuth()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

    expect(mockCloseAll).not.toHaveBeenCalled()
    expect(mockResetTunnels).not.toHaveBeenCalled()
  })

  it('logout survives a sidecar tunnel-reset failure', async () => {
    // resetTunnels is best-effort: a rejected sidecar RPC (e.g. the sidecar is
    // restarting) must be logged, not break the logout flow or surface as an
    // unhandled rejection.
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', orgId: 'o1', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockResetTunnels.mockRejectedValueOnce(new Error('sidecar gone'))

    await auth().logout()
    // Let the rejected resetTunnels promise settle through its .catch.
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(screen.getByTestId('authenticated')).toHaveTextContent('no')
  })
})
