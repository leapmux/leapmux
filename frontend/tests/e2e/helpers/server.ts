import type { ChildProcess } from 'node:child_process'
import type { AddressInfo } from 'node:net'
import type { E2EGlobalState } from '../global-setup'
import { readFileSync } from 'node:fs'
import { createServer, isIPv6 } from 'node:net'
import { join } from 'node:path'
import process from 'node:process'
import { E2E_BROWSER_HOST, fetchHub } from './hubTransport'

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
  return waitForHttpReady(url, timeoutMs)
}

function waitForHttpReady(url: string, timeoutMs: number, proc?: ChildProcess): Promise<void> {
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

    const onError = (error: Error) => finish(error)
    const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
      finish(new Error(`The hub at ${url} exited before startup completed: code=${code ?? 'none'} signal=${signal ?? 'none'}`))
    }
    proc?.once('error', onError)
    proc?.once('exit', onExit)
    if (proc && (proc.exitCode !== null || proc.signalCode !== null))
      onExit(proc.exitCode, proc.signalCode)

    function finish(error?: Error) {
      if (finished)
        return
      finished = true
      clearTimeout(deadline)
      clearTimeout(retry)
      // Stop requests that never return headers. Cancellation releases the body after a successful response.
      controller.abort()
      proc?.removeListener('error', onError)
      proc?.removeListener('exit', onExit)
      if (error)
        reject(error)
      else
        resolve()
    }

    async function check() {
      try {
        const response = await fetchHub(url, { signal: controller.signal })
        if (finished)
          return
        if (response.ok) {
          if (proc !== undefined)
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

/** The ordered listen endpoints a hub's state file lists. */
export interface ResolvedListenAddresses {
  /** TCP addresses in bind order, each carrying the port its listener resolved to. */
  tcp: string[]
  /** Local IPC endpoint URLs in bind order (`unix:`, `npipe:`). */
  local: string[]
}

/**
 * Read the resolved bind set from the hub's state file: the TCP addresses and the local IPC endpoint URLs, each in bind
 * order. Two identical port-zero requests resolve to two ports, so the answer is per request occurrence rather than per
 * spelling.
 */
export function resolvedHubListenAddresses(raw: string): ResolvedListenAddresses {
  const state = JSON.parse(raw) as { listen?: unknown } | null
  const listen = state?.listen
  if (!Array.isArray(listen) || !listen.every(entry => typeof entry === 'string'))
    throw new Error('expected a listen list of address strings in the hub state file')
  const tcp: string[] = []
  const local: string[] = []
  for (const entry of listen) {
    const localPrefix = /^(?:unix|npipe):/.exec(entry)?.[0]
    if (localPrefix !== undefined) {
      if (entry.length === localPrefix.length)
        throw new Error(`local IPC address ${entry} has no target`)
      local.push(entry)
      continue
    }
    const separator = entry.lastIndexOf(':')
    const portText = entry.slice(separator + 1)
    const port = Number(portText)
    if (separator < 0 || !/^\d+$/.test(portText) || !Number.isInteger(port) || port < 1 || port > 65535)
      throw new Error(`the TCP address ${entry} has no usable port`)
    const host = entry.slice(0, separator)
    const bracketedIPv6 = /^\[([^%[\]]+)(?:%[^[\]]+)?\]$/.exec(host)?.[1]
    const validHost = bracketedIPv6 === undefined
      ? !/[:[\]\s]/.test(host)
      : isIPv6(bracketedIPv6)
    if (!validHost)
      throw new Error(`invalid TCP address ${entry} in the hub state file`)
    tcp.push(entry)
  }
  return { tcp, local }
}

/** Read the primary (first) resolved TCP address from the hub's state file. */
export function resolvedHubTCPFromStateJson(raw: string): string {
  const entry = resolvedHubListenAddresses(raw).tcp[0]
  if (entry === undefined)
    throw new Error('expected at least one TCP address in the hub state file\'s listen list')
  return entry
}

/** Give a browser host the assigned port from the hub's state file. */
export function hubUrlFromStateJson(raw: string, browserHost = E2E_BROWSER_HOST): string {
  const entry = resolvedHubTCPFromStateJson(raw)
  return `http://${browserHost}:${entry.slice(entry.lastIndexOf(':') + 1)}`
}

/** Parse one state-file snapshot into the complete result a started Hub exposes. */
export function startedHubFromStateJson(raw: string, browserHost = E2E_BROWSER_HOST): StartedHub {
  const { tcp, local } = resolvedHubListenAddresses(raw)
  const listen = tcp[0]
  if (listen === undefined)
    throw new Error('expected at least one TCP address in the hub state file\'s listen list')
  return {
    hubUrl: `http://${browserHost}:${listen.slice(listen.lastIndexOf(':') + 1)}`,
    listen,
    tcp,
    local,
  }
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

/** The addresses of a hub that started: the URL that a browser reaches, the primary TCP address, and the resolved endpoints in bind order. */
export interface StartedHub extends ResolvedListenAddresses {
  hubUrl: string
  /** The primary (first) resolved TCP address: what a browser connects to. */
  listen: string
}

/**
 * Wait for a spawned hub to write its state file and to answer HTTP, or fail when its process exits.
 * A hub that listens on port 0 gets its port from the operating system, and the state file is the only record of it.
 * `browserHost` replaces the bound host in `hubUrl`, so the URL matches the domain of the session cookies.
 * The browser URL follows the FIRST TCP address; the full ordered endpoints stay on the result.
 */
export async function waitForHubStart(statePath: string, proc: ChildProcess, browserHost = E2E_BROWSER_HOST): Promise<StartedHub> {
  const state = await waitForHubStateFile(statePath, proc)
  const result = startedHubFromStateJson(state, browserHost)
  const { hubUrl } = result
  await waitForHubReady(hubUrl, proc)
  return result
}

/** Wait until the hub responds or its process exits. */
export function waitForHubReady(url: string, proc: ChildProcess, timeoutMs = 30_000): Promise<void> {
  return waitForHttpReady(url, timeoutMs, proc)
}
