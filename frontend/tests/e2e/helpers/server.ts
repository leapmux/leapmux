import type { AddressInfo } from 'node:net'
import type { E2EGlobalState } from '../global-setup'
import { readFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { join } from 'node:path'
import process from 'node:process'

// Dev mode stores the hub database below the fixture's hub subdirectory.
// Share this path rule between fixtures and tests.
export function hubDataDir(dataDir: string): string {
  return join(dataDir, 'hub')
}

/**
 * Supply the environment for every test hub.
 * Clear LEAPMUX_HUB_DEV_FRONTEND so the hub serves the binary's embedded frontend.
 * An inherited development URL could select a different checkout or an unavailable server.
 * A test could then pass against code outside this build, or fail for an unrelated cause.
 * Apply the restriction after caller overrides. The parameter type also excludes this setting.
 */
export function hubSpawnEnv(
  extra: Omit<Record<string, string | undefined>, 'LEAPMUX_HUB_DEV_FRONTEND'> = {},
): NodeJS.ProcessEnv {
  return { ...process.env, ...extra, LEAPMUX_HUB_DEV_FRONTEND: undefined }
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
