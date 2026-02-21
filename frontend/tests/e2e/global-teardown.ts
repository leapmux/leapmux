/* eslint-disable no-console */
import type { E2EGlobalState } from './global-setup'
import { existsSync, readdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import process from 'node:process'

function findStatePath(): string | null {
  if (process.env.E2E_STATE_PATH && existsSync(process.env.E2E_STATE_PATH)) {
    return process.env.E2E_STATE_PATH
  }

  const tmp = tmpdir()
  try {
    for (const dir of readdirSync(tmp)) {
      if (dir.startsWith('leapmux-e2e-')) {
        const candidate = join(tmp, dir, 'e2e-state.json')
        if (existsSync(candidate))
          return candidate
      }
    }
  }
  catch {
    // ignore
  }

  return null
}

export default async function globalTeardown() {
  const statePath = findStatePath()
  if (!statePath) {
    console.log('[e2e] No state file found, nothing to tear down')
    return
  }

  console.log(`[e2e] Reading state from ${statePath}`)
  const state: E2EGlobalState = JSON.parse(readFileSync(statePath, 'utf-8'))

  // Clean up temp directory
  try {
    rmSync(state.tmpDir, { recursive: true, force: true })
    console.log(`[e2e] Cleaned up ${state.tmpDir}`)
  }
  catch (err) {
    console.warn(`[e2e] Failed to clean up ${state.tmpDir}:`, err)
  }

  console.log('[e2e] Global teardown complete')
}
