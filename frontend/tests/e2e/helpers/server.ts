import type { AddressInfo } from 'node:net'
import type { E2EGlobalState } from '../global-setup'
import { readFileSync } from 'node:fs'
import { createServer } from 'node:net'
import process from 'node:process'

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

/**
 * Poll `url` until it answers.
 *
 * The timeout is the ceiling for a server that never comes up; the interval is
 * what every healthy run actually pays. A locally spawned instance binds in
 * tens of milliseconds, so a half-second tick spent nearly all of its time
 * asleep after the server was already listening.
 */
export function waitForServer(url: string, timeoutMs = 30_000): Promise<void> {
  const start = Date.now()
  return new Promise((resolve, reject) => {
    const check = () => {
      fetch(url).then(() => resolve()).catch(() => {
        if (Date.now() - start > timeoutMs) {
          reject(new Error(`Server at ${url} did not start within ${timeoutMs}ms`))
        }
        else {
          setTimeout(check, 25)
        }
      })
    }
    check()
  })
}
