/* eslint-disable no-console */
import { execSync } from 'node:child_process'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const ROOT = resolve(__dirname, '..', '..', '..')

export interface E2EGlobalState {
  binaryPath: string
  tmpDir: string
}

export default async function globalSetup() {
  // Verify tests were launched via `bun run test:e2e` (scripts/run-e2e.ts).
  // The runner writes a nonce file and passes the path + expected value via env vars.
  // This prevents bypassing the guard by simply setting LEAPMUX_E2E_RUNNER=1.
  const noncePath = process.env.LEAPMUX_E2E_NONCE_PATH
  const expectedNonce = process.env.LEAPMUX_E2E_NONCE
  if (!noncePath || !expectedNonce) {
    throw new Error(
      'E2E tests must be run via `bun run test:e2e` (scripts/run-e2e.ts), '
      + 'not directly with `bunx playwright test`. '
      + 'The runner script ensures a clean build before running tests.',
    )
  }
  try {
    const actualNonce = readFileSync(noncePath, 'utf-8').trim()
    if (actualNonce !== expectedNonce) {
      throw new Error('nonce mismatch')
    }
  }
  catch {
    throw new Error(
      'E2E tests must be run via `bun run test:e2e` (scripts/run-e2e.ts), '
      + 'not directly with `bunx playwright test`. '
      + 'The runner script ensures a clean build before running tests.',
    )
  }

  const tmpDir = mkdtempSync(join(tmpdir(), 'leapmux-e2e-'))

  console.log(`[e2e] Temp dir: ${tmpDir}`)

  // Build the leapmux binary (includes embedded frontend)
  console.log('[e2e] Building leapmux...')
  execSync('task build-backend', { cwd: ROOT, stdio: 'inherit' })

  const binaryPath = join(ROOT, 'leapmux')

  // Save minimal state for fixtures and teardown
  const state: E2EGlobalState = {
    binaryPath,
    tmpDir,
  }

  const statePath = join(tmpDir, 'e2e-state.json')
  writeFileSync(statePath, JSON.stringify(state, null, 2))

  // Store the state path in an env var so fixtures and teardown can find it
  process.env.E2E_STATE_PATH = statePath

  console.log('[e2e] Global setup complete')
}
