import type { ParentComponent } from 'solid-js'
import type { User } from '~/generated/leapmux/v1/auth_pb'
import { create } from '@bufbuild/protobuf'
import { createContext, createEffect, createSignal, on, onMount, useContext } from 'solid-js'
import { authClient } from '~/api/clients'
import { platformBridge } from '~/api/platformBridge'
import { loadTimeouts, setOnAuthError } from '~/api/transport'
import { channelManager } from '~/api/workerRpc'
import { LoginRequestSchema } from '~/generated/leapmux/v1/auth_pb'
import { formatErrorMessage } from '~/lib/errors'
import { createLogger } from '~/lib/logger'
import { isSoloMode, loadSystemInfo } from '~/lib/systemInfo'

const log = createLogger('auth')

export interface AuthState {
  user: () => User | null
  loading: () => boolean
  error: () => string | null
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  setAuth: (user: User) => void
  refreshUser: () => Promise<void>
  isAuthenticated: () => boolean
}

const AuthContext = createContext<AuthState>()

export const AuthProvider: ParentComponent = (props) => {
  const [user, setUser] = createSignal<User | null>(null)
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)

  /**
   * Drop every pooled E2EE channel on an identity change.
   *
   * Channels are pooled per worker for up to an hour and carry the identity the Hub
   * authenticated them as at OPEN time, and `channelManager` is a module-level
   * singleton that outlives this provider. So without this, logging out and back in as
   * someone else keeps serving the previous user's channels: every worker RPC the new
   * user's page issues runs on the worker AS THE OLD USER, for the rest of the
   * channels' hour. getOrOpenChannel also re-checks the identity before reusing a
   * pooled channel; that is the backstop, this is the eager release -- it also frees
   * the shared WebSocket, which the logged-out user has no business holding open.
   *
   * On the desktop this also resets the SIDECAR's tunnels. In distributed mode the Go
   * sidecar pools its own E2EE channels and runs tunnel listeners bound to the
   * connection (not the browser session), and it authenticates to the Hub purely by
   * the proxy's cookie jar -- it has no user identity to key an eager close on. So
   * without resetTunnels, a user switch would leave the previous user's tunnels
   * relaying (and their cached channels reusable) under the old identity until the Hub
   * revokes the session. resetTunnels is a no-op off the desktop and best-effort: a
   * failure must not break the logout flow, but it must not pass silently either.
   */
  const closePooledChannels = () => {
    channelManager.closeAll()
    platformBridge.resetTunnels().catch((err) => {
      log.warn('failed to reset sidecar tunnels on identity change', { error: String(err) })
    })
  }

  // Drive the eager release off the `user` signal itself, so EVERY identity
  // transition -- logout, an auth error, a login over a live session, and any future
  // setUser path (setAuth, refreshUser, an impersonation seed) -- releases by
  // construction rather than each transition site remembering to call it. Fires only
  // when a PREVIOUS authenticated user gives way to a different id or to logout: the
  // initial null -> first-user restore (prev falsy) and a same-user refresh
  // (prev.id === next id) must not churn the pool or fire a spurious resetTunnels.
  createEffect(on(user, (u, prev) => {
    if (prev && prev.id !== (u?.id ?? ''))
      closePooledChannels()
  }, { defer: true }))

  // Register auth error callback for auto-logout on 401.
  setOnAuthError(() => {
    if (!isSoloMode())
      setUser(null)
  })

  onMount(async () => {
    await loadSystemInfo()

    // Try to restore session from cookie (both solo and multi-user modes).
    try {
      const resp = await authClient.getCurrentUser({})
      setUser(resp.user ?? null)
      loadTimeouts().catch(() => {})
    }
    catch {
      // No valid session — user needs to log in.
    }
    setLoading(false)
  })

  const login = async (username: string, password: string) => {
    setError(null)
    setLoading(true)
    try {
      const req = create(LoginRequestSchema, { username, password })
      const resp = await authClient.login(req)
      // Logging in over a still-authenticated session (a bookmarked /login, a stale
      // tab) is an identity transition just like logout: setUser drives the eager
      // release of the previous user's pooled channels through the createEffect above,
      // rather than leaving them to the lazy per-request identity check (which evicts
      // one channel per request while the shared WebSocket stays held for the old user).
      setUser(resp.user ?? null)
      loadTimeouts().catch(() => {})
    }
    catch (e) {
      const msg = formatErrorMessage(e, 'Login failed')
      setError(msg)
      throw e
    }
    finally {
      setLoading(false)
    }
  }

  const logout = async () => {
    if (isSoloMode())
      return
    try {
      await authClient.logout({})
    }
    catch {
      // Ignore logout errors.
    }
    finally {
      setUser(null)
    }
  }

  const setAuth = (u: User) => {
    setUser(u)
    setLoading(false)
  }

  const refreshUser = async () => {
    try {
      const resp = await authClient.getCurrentUser({})
      setUser(resp.user ?? null)
    }
    catch {
      // Ignore — user state unchanged.
    }
  }

  const state: AuthState = {
    user,
    loading,
    error,
    login,
    logout,
    setAuth,
    refreshUser,
    isAuthenticated: () => user() !== null,
  }

  return (
    <AuthContext.Provider value={state}>
      {props.children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuth must be used within AuthProvider')
  }
  return ctx
}
