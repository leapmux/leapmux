import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const runnerRequired = 'Run end-to-end tests with `bun run test:e2e` so the launcher verifies the build.'

export interface E2EGlobalState {
  binaryPath: string
  tmpDir: string
}

export default async function globalSetup() {
  // The launcher builds once and gives each run a private directory and nonce.
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
  const state: E2EGlobalState = {
    binaryPath: join(root, process.platform === 'win32' ? 'leapmux.exe' : 'leapmux'),
    tmpDir,
  }
  const statePath = join(tmpDir, 'e2e-state.json')
  writeFileSync(statePath, JSON.stringify(state))
  process.env.E2E_STATE_PATH = statePath
}
