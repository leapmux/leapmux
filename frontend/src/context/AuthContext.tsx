import type { ParentComponent } from 'solid-js'
import type { User } from '~/generated/leapmux/v1/auth_pb'
import { create } from '@bufbuild/protobuf'
import { createContext, createSignal, onMount, useContext } from 'solid-js'
import { authClient } from '~/api/clients'
import { clearToken, getToken, loadTimeouts, setOnAuthError, setToken } from '~/api/transport'
import { LoginRequestSchema } from '~/generated/leapmux/v1/auth_pb'

interface AuthState {
  user: () => User | null
  loading: () => boolean
  error: () => string | null
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  setAuth: (token: string, user: User) => void
  isAuthenticated: () => boolean
}

const AuthContext = createContext<AuthState>()

export const AuthProvider: ParentComponent = (props) => {
  const [user, setUser] = createSignal<User | null>(null)
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)

  // Register auth error callback for auto-logout on 401.
  setOnAuthError(() => {
    setUser(null)
  })

  onMount(async () => {
    const token = getToken()
    if (token) {
      try {
        const resp = await authClient.getCurrentUser({})
        setUser(resp.user ?? null)
        // Load timeout configuration after successful auth validation.
        loadTimeouts().catch(() => {})
      }
      catch {
        clearToken()
      }
    }
    setLoading(false)
  })

  const login = async (username: string, password: string) => {
    setError(null)
    setLoading(true)
    try {
      const req = create(LoginRequestSchema, { username, password })
      const resp = await authClient.login(req)
      setToken(resp.token)
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
    try {
      await authClient.logout({})
    }
    catch {
      // Ignore logout errors.
    }
    finally {
      clearToken()
      setUser(null)
    }
  }

  const setAuth = (token: string, u: User) => {
    setToken(token)
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
