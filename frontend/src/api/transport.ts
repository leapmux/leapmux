import type { Interceptor } from '@connectrpc/connect'
import { Code, ConnectError } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'

const TOKEN_KEY = 'leapmux_token'

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token)
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY)
}

// Callbacks for auth state changes (set by AuthContext)
let onAuthError: (() => void) | null = null

export function setOnAuthError(callback: () => void): void {
  onAuthError = callback
}

const authInterceptor: Interceptor = next => async (req) => {
  const token = getToken()
  if (token) {
    req.header.set('Authorization', `Bearer ${token}`)
  }

  try {
    return await next(req)
  }
  catch (err) {
    // Auto-logout on unauthenticated errors (expired/invalid token)
    if (err instanceof ConnectError && err.code === Code.Unauthenticated) {
      clearToken()
      onAuthError?.()
    }
    throw err
  }
}

export const transport = createConnectTransport({
  baseUrl: window.location.origin,
  interceptors: [authInterceptor],
  defaultTimeoutMs: 30_000,
})
