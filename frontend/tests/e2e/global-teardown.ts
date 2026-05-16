/* eslint-disable no-console */
import type { E2EGlobalState } from './global-setup'
import { existsSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs'
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

/**
 * Sweep the leapmux-e2e-separate-* tmp dirs left over from
 * `separateHubWorker` fixtures and kill any tracked PIDs that are still
 * alive. `process-control-fixtures.ts:trackSpawnedPid` writes one PID
 * per spawn (initial hub, initial worker, every restartHub /
 * restartWorker) so we catch detached children that survived a crash,
 * a timeout, or a restart that threw before its mutableState update.
 */
function sweepLeftoverFixtureProcesses() {
  const tmp = tmpdir()
  let dirs: string[] = []
  try {
    dirs = readdirSync(tmp).filter(d => d.startsWith('leapmux-e2e-separate-'))
  }
  catch {
    return
  }
  for (const dir of dirs) {
    const fixtureDir = join(tmp, dir)
    const pidsPath = join(fixtureDir, 'pids.json')
    if (!existsSync(pidsPath))
      continue
    let pids: number[] = []
    try {
      pids = JSON.parse(readFileSync(pidsPath, 'utf-8'))
    }
    catch {
      continue
    }
    for (const pid of pids) {
      try {
        process.kill(pid, 'SIGTERM')
        console.log(`[e2e] Swept leaked process pid=${pid} (from ${dir})`)
      }
      catch { /* already gone */ }
    }
    // Best-effort SIGKILL pass for anything that ignored SIGTERM.
    if (pids.length > 0) {
      setTimeout(() => {
        for (const pid of pids) {
          try {
            process.kill(pid, 'SIGKILL')
          }
          catch { /* already gone */ }
        }
      }, 500)
    }
    // Drop the fixture's whole tmp dir — the per-fixture teardown
    // normally does this; if it didn't (crash, killed worker), do it
    // here so subsequent runs don't accumulate stale state.
    try {
      const st = statSync(fixtureDir)
      if (st.isDirectory())
        rmSync(fixtureDir, { recursive: true, force: true })
    }
    catch { /* gone */ }
  }
}

export default async function globalTeardown() {
  // Sweep first so any survivors get killed even if we have no
  // global state file (e.g. global-setup never wrote one because the
  // run aborted very early).
  sweepLeftoverFixtureProcesses()

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
