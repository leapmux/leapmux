import type { SuiteServerState } from './helpers/suiteServer'
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, join } from 'node:path'
import process from 'node:process'
import { runBinaryPath } from './helpers/runBinary'
import { isInsideDirectory, RUN_ROOT_ENV } from './helpers/runRoot'
import { startSuiteServer } from './helpers/suiteServer'

const runnerRequired = 'Run end-to-end tests with `bun run test:e2e` so the launcher verifies the build.'

export interface E2EGlobalState extends SuiteServerState {
  binaryPath: string
  tmpDir: string
  /** The run root that holds `tmpDir` (./helpers/runRoot.ts). */
  runRoot: string
}

export default async function globalSetup(): Promise<(() => Promise<void>) | undefined> {
  // The launcher builds once and gives each run a private directory, a nonce,
  // and a private copy of the binary.
  const noncePath = process.env.LEAPMUX_E2E_NONCE_PATH
  const expectedNonce = process.env.LEAPMUX_E2E_NONCE
  if (!noncePath || !expectedNonce)
    throw new Error(runnerRequired)
  try {
    if (readFileSync(noncePath, 'utf8').trim() !== expectedNonce)
      throw new Error('Nonce mismatch')
  }
  catch {
    throw new Error(runnerRequired)
  }

  const tmpDir = dirname(noncePath)
  // The launcher states the run root beside the nonce, and the private directory of this run lies in it.
  const runRoot = process.env[RUN_ROOT_ENV]
  if (!runRoot || !isAbsolute(runRoot) || !isInsideDirectory(tmpDir, runRoot))
    throw new Error(runnerRequired)
  const baseState = {
    // Never the build output at the repository root. See runBinaryPath.
    binaryPath: runBinaryPath(tmpDir),
    tmpDir,
    runRoot,
  }
  const server = await startSuiteServer(baseState)
  const state: E2EGlobalState = { ...baseState, ...server.state }
  const statePath = join(tmpDir, 'e2e-state.json')
  try {
    writeFileSync(statePath, JSON.stringify(state))
    process.env.E2E_STATE_PATH = statePath
    process.env.PI_CODING_AGENT_DIR = state.piAgentDir
    return server.stop
  }
  catch (error) {
    await server.stop()
    throw error
  }
}
