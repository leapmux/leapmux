import type { CallOptions, Interceptor } from '@connectrpc/connect'
import { Code, ConnectError, createClient } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { UserService } from '~/generated/leapmux/v1/user_pb'

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

// ---------------------------------------------------------------------------
// Dynamic timeout configuration
// ---------------------------------------------------------------------------

/** Multiplier applied to backend timeouts for frontend RPC deadlines. */
const TIMEOUT_MULTIPLIER = 1.5

export interface TimeoutConfig {
  apiTimeoutSeconds: number
  agentStartupTimeoutSeconds: number
  worktreeCreateTimeoutSeconds: number
  worktreeDeleteTimeoutSeconds: number
}

const timeoutConfig: TimeoutConfig = {
  apiTimeoutSeconds: 10,
  agentStartupTimeoutSeconds: 30,
  worktreeCreateTimeoutSeconds: 60,
  worktreeDeleteTimeoutSeconds: 60,
}

/** Load timeout configuration from the server. Call after authentication. */
export async function loadTimeouts(): Promise<void> {
  try {
    const client = createClient(UserService, transport)
    const resp = await client.getTimeouts({})
    if (resp.apiTimeoutSeconds > 0)
      timeoutConfig.apiTimeoutSeconds = resp.apiTimeoutSeconds
    if (resp.agentStartupTimeoutSeconds > 0)
      timeoutConfig.agentStartupTimeoutSeconds = resp.agentStartupTimeoutSeconds
    if (resp.worktreeCreateTimeoutSeconds > 0)
      timeoutConfig.worktreeCreateTimeoutSeconds = resp.worktreeCreateTimeoutSeconds
    if (resp.worktreeDeleteTimeoutSeconds > 0)
      timeoutConfig.worktreeDeleteTimeoutSeconds = resp.worktreeDeleteTimeoutSeconds
  }
  catch {
    // Use defaults if the server doesn't support this endpoint yet.
  }
}

/** Get the current timeout config (for admin settings display). */
export function getTimeoutConfig(): Readonly<TimeoutConfig> {
  return timeoutConfig
}

// ---------------------------------------------------------------------------
// Per-call timeout helpers (return CallOptions with timeoutMs)
// ---------------------------------------------------------------------------

/** RPC timeout for agent operations (start/resume + message delivery). */
export function agentCallTimeout(agentActive: boolean): CallOptions {
  const backendSec = agentActive
    ? timeoutConfig.apiTimeoutSeconds
    : timeoutConfig.agentStartupTimeoutSeconds + timeoutConfig.apiTimeoutSeconds
  return { timeoutMs: Math.ceil(TIMEOUT_MULTIPLIER * backendSec * 1000) }
}

/** RPC timeout for worktree creation. */
export function worktreeCreateCallTimeout(): CallOptions {
  return { timeoutMs: Math.ceil(TIMEOUT_MULTIPLIER * timeoutConfig.worktreeCreateTimeoutSeconds * 1000) }
}

/** RPC timeout for worktree deletion / status check. */
export function worktreeDeleteCallTimeout(): CallOptions {
  return { timeoutMs: Math.ceil(TIMEOUT_MULTIPLIER * timeoutConfig.worktreeDeleteTimeoutSeconds * 1000) }
}

// ---------------------------------------------------------------------------
// Loading indicator timeout helpers (return milliseconds)
// ---------------------------------------------------------------------------

/** Loading timeout for agent operations. */
export function agentLoadingTimeoutMs(agentActive: boolean): number {
  const backendSec = agentActive
    ? timeoutConfig.apiTimeoutSeconds
    : timeoutConfig.agentStartupTimeoutSeconds + timeoutConfig.apiTimeoutSeconds
  return Math.ceil(TIMEOUT_MULTIPLIER * backendSec * 1000)
}

/** Loading timeout for worktree creation. */
export function worktreeCreateLoadingTimeoutMs(): number {
  return Math.ceil(TIMEOUT_MULTIPLIER * timeoutConfig.worktreeCreateTimeoutSeconds * 1000)
}

/** Loading timeout for worktree deletion. */
export function worktreeDeleteLoadingTimeoutMs(): number {
  return Math.ceil(TIMEOUT_MULTIPLIER * timeoutConfig.worktreeDeleteTimeoutSeconds * 1000)
}

/** Loading timeout for general API calls. */
export function apiLoadingTimeoutMs(): number {
  return Math.ceil(TIMEOUT_MULTIPLIER * timeoutConfig.apiTimeoutSeconds * 1000)
}
