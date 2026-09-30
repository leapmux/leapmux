import type { ChildProcess } from 'node:child_process'
import type { AddressInfo } from 'node:net'
import type { E2EGlobalState } from '../global-setup'
import { readFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { join } from 'node:path'
import process from 'node:process'

/** The browser origin host and the session-cookie domain for shared E2E servers. */
export const E2E_BROWSER_HOST = 'localhost'

// Dev mode stores the hub database below the fixture's hub subdirectory.
// Share this path rule between fixtures and tests.
export function hubDataDir(dataDir: string): string {
  return join(dataDir, 'hub')
}

/**
 * Supply the environment for every test hub and worker.
 *
 * Merges the isolated agent configuration that global setup wrote, so a hub a
 * test starts routes its agents to the mock endpoint exactly as the shared one
 * does. WITHOUT it those agents inherit the developer's real provider
 * credentials from `process.env` and contact the live endpoint. No assertion
 * catches that, because a real model answers a test prompt correctly — the only
 * symptom is a slow, non-deterministic run and a bill.
 *
 * Clear LEAPMUX_HUB_DEV_FRONTEND so the hub serves the binary's embedded frontend.
 * An inherited development URL could select a different checkout or an unavailable server.
 * A test could then pass against code outside this build, or fail for an unrelated cause.
 * Apply the restriction after caller overrides. The parameter type also excludes this setting.
 */
export function hubSpawnEnv(
  extra: Omit<Record<string, string | undefined>, 'LEAPMUX_HUB_DEV_FRONTEND'> = {},
): NodeJS.ProcessEnv {
  return {
    ...process.env,
    ...mockAgentEnv(),
    ...extra,
    // Cursor sends the loopback mock's HTTP/2 calls through HTTP_PROXY even when
    // NO_PROXY lists loopback. Remove inherited and caller-supplied proxies.
    HTTP_PROXY: undefined,
    http_proxy: undefined,
    ALL_PROXY: undefined,
    all_proxy: undefined,
    LEAPMUX_HUB_DEV_FRONTEND: undefined,
  }
}

/**
 * The isolated agent configuration, or nothing before global setup wrote it.
 *
 * `startSuiteServer` itself spawns the shared hub before the state file exists,
 * and it passes the same map explicitly. Every later spawn reads it from here.
 */
export function mockAgentEnv(): Record<string, string> {
  if (!process.env.E2E_STATE_PATH)
    return {}
  return getGlobalState().agentEnv
}
// ──────────────────────────────────────────────
// Global state (read from file written by global-setup)
// ──────────────────────────────────────────────

let cachedGlobalState: E2EGlobalState | null = null

export function getGlobalState(): E2EGlobalState {
  if (cachedGlobalState)
    return cachedGlobalState

  const statePath = process.env.E2E_STATE_PATH
  if (!statePath)
    throw new Error('E2E_STATE_PATH env var is not set')

  cachedGlobalState = JSON.parse(readFileSync(statePath, 'utf-8'))
  return cachedGlobalState!
}

// ──────────────────────────────────────────────
// Server utilities
// ──────────────────────────────────────────────

export function findFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer()
    server.listen(0, () => {
      const { port } = server.address() as AddressInfo
      server.close(() => resolve(port))
    })
    server.on('error', reject)
  })
}

/** Wait for a successful HTTP response within the startup deadline. */
export function waitForServer(url: string, timeoutMs = 30_000): Promise<void> {
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0 || timeoutMs > 2_147_483_647)
    return Promise.reject(new RangeError('The startup deadline must fit a positive Node timer delay'))
  return new Promise((resolve, reject) => {
    const controller = new AbortController()
    let finished = false
    let retry: ReturnType<typeof setTimeout> | undefined
    let lastError: unknown
    const deadline = setTimeout(() => {
      finish(new Error(`Server at ${url} did not start within ${timeoutMs}ms`, { cause: lastError }))
    }, timeoutMs)

    function finish(error?: Error) {
      if (finished)
        return
      finished = true
      clearTimeout(deadline)
      clearTimeout(retry)
      // Stop requests that never return headers. Cancellation releases the body after a successful response.
      controller.abort()
      if (error)
        reject(error)
      else
        resolve()
    }

    async function check() {
      try {
        const response = await fetch(url, { signal: controller.signal })
        if (finished)
          return
        if (response.ok) {
          finish()
          return
        }
        await response.body?.cancel()
        lastError = new Error(`Startup request returned HTTP ${response.status}`)
      }
      catch (error) {
        lastError = error
      }
      if (!finished)
        retry = setTimeout(check, 25)
    }
    void check()
  })
}

/** Read the one resolved TCP address from the hub's state file. */
export function resolvedHubTCPFromStateJson(raw: string): string {
  const state = JSON.parse(raw) as { listen?: unknown } | null
  const listen = state?.listen
  if (!Array.isArray(listen) || !listen.every(entry => typeof entry === 'string'))
    throw new Error('expected one TCP address in the hub state file\'s listen list')
  const tcp = listen.filter(entry => !entry.startsWith('unix:') && !entry.startsWith('npipe:'))
  const entry = tcp[0]
  if (tcp.length !== 1 || entry === undefined)
    throw new Error(`expected one TCP address in the hub state file's listen list, got ${JSON.stringify(listen)}`)
  const separator = entry.lastIndexOf(':')
  const portText = entry.slice(separator + 1)
  const port = Number(portText)
  if (separator < 0 || !/^\d+$/.test(portText) || !Number.isInteger(port) || port < 1 || port > 65535)
    throw new Error(`the TCP address ${entry} names no usable port`)
  return entry
}

/** Give a browser host the assigned port from the hub's state file. */
export function hubUrlFromStateJson(raw: string, browserHost = E2E_BROWSER_HOST): string {
  const entry = resolvedHubTCPFromStateJson(raw)
  return `http://${browserHost}:${entry.slice(entry.lastIndexOf(':') + 1)}`
}

/** Read the hub's state file once it appears, or fail when the process exits. */
export function waitForHubStateFile(statePath: string, proc: ChildProcess, timeoutMs = 30_000): Promise<string> {
  return new Promise((resolve, reject) => {
    let finished = false
    let retry: ReturnType<typeof setTimeout> | undefined
    const deadline = setTimeout(() => finish(undefined, new Error(`The hub wrote no state file at ${statePath} within ${timeoutMs}ms`)), timeoutMs)
    const onError = (error: Error) => finish(undefined, error)
    const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
      try {
        finish(readFileSync(statePath, 'utf8'))
      }
      catch {
        finish(undefined, new Error(`The hub exited before it wrote ${statePath}: code=${code ?? 'none'} signal=${signal ?? 'none'}`))
      }
    }
    proc.once('error', onError)
    proc.once('exit', onExit)
    if (proc.exitCode !== null || proc.signalCode !== null)
      onExit(proc.exitCode, proc.signalCode)

    function finish(content: string | undefined, error?: Error) {
      if (finished)
        return
      finished = true
      clearTimeout(deadline)
      clearTimeout(retry)
      proc.removeListener('error', onError)
      proc.removeListener('exit', onExit)
      if (error)
        reject(error)
      else
        resolve(content as string)
    }

    function check() {
      try {
        finish(readFileSync(statePath, 'utf8'))
      }
      catch {
        if (!finished)
          retry = setTimeout(check, 25)
      }
    }
    check()
  })
}

/** Wait until the hub responds or its process exits. */
export function waitForHubReady(url: string, proc: ChildProcess, timeoutMs = 30_000): Promise<void> {
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0 || timeoutMs > 2_147_483_647)
    return Promise.reject(new RangeError('The startup deadline must fit a positive Node timer delay'))
  return new Promise((resolve, reject) => {
    const controller = new AbortController()
    let finished = false
    let retry: ReturnType<typeof setTimeout> | undefined
    let lastError: unknown
    const deadline = setTimeout(() => finish(new Error(`Server at ${url} did not start within ${timeoutMs}ms`, { cause: lastError })), timeoutMs)

    const onError = (error: Error) => finish(error)
    const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
      finish(new Error(`The hub at ${url} exited before startup completed: code=${code ?? 'none'} signal=${signal ?? 'none'}`))
    }
    proc.once('error', onError)
    proc.once('exit', onExit)
    if (proc.exitCode !== null || proc.signalCode !== null)
      onExit(proc.exitCode, proc.signalCode)

    function finish(error?: Error) {
      if (finished)
        return
      finished = true
      clearTimeout(deadline)
      clearTimeout(retry)
      controller.abort()
      proc.removeListener('error', onError)
      proc.removeListener('exit', onExit)
      if (error)
        reject(error)
      else
        resolve()
    }

    async function check() {
      try {
        const response = await fetch(url, { signal: controller.signal })
        if (finished)
          return
        if (response.ok) {
          await response.body?.cancel()
          finish()
          return
        }
        await response.body?.cancel()
        lastError = new Error(`Startup request returned HTTP ${response.status}`)
      }
      catch (error) {
        lastError = error
      }
      if (!finished)
        retry = setTimeout(check, 25)
    }
    void check()
  })
}
