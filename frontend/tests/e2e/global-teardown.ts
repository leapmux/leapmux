import { dirname } from 'node:path'
import process from 'node:process'
import { stopTrackedProcesses } from './helpers/processRegistry'

export default async function globalTeardown() {
  const statePath = process.env.E2E_STATE_PATH
  if (!statePath)
    return
  // The launcher owns this directory and removes it after Playwright exits.
  // Never search for another run's state when this run has no state.
  await stopTrackedProcesses(dirname(statePath))
}
