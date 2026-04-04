import type { ParentComponent } from 'solid-js'
import type { User } from '~/generated/leapmux/v1/auth_pb'
import { create } from '@bufbuild/protobuf'
import { createContext, createSignal, onMount, useContext } from 'solid-js'
import { authClient } from '~/api/clients'
import { loadTimeouts, setOnAuthError } from '~/api/transport'
import { LoginRequestSchema } from '~/generated/leapmux/v1/auth_pb'
import { isSoloMode, loadSystemInfo } from '~/lib/systemInfo'

interface AuthState {
  user: () => User | null
  loading: () => boolean
  error: () => string | null
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  setAuth: (user: User) => void
  isAuthenticated: () => boolean
}

const AuthContext = createContext<AuthState>()

export const AuthProvider: ParentComponent = (props) => {
  const [user, setUser] = createSignal<User | null>(null)
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)

  // Register auth error callback for auto-logout on 401.
  setOnAuthError(() => {
    if (!isSoloMode()) {
      setUser(null)
    }
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
      setUser(resp.user ?? null)
      loadTimeouts().catch(() => {})
    }
    catch (e) {
      const msg = e instanceof Error ? e.message : 'Login failed'
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

  const state: AuthState = {
    user,
    loading,
    error,
    login,
    logout,
    setAuth,
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
