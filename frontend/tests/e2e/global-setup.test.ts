import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import globalSetup from './global-setup'

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'global-setup-test-'))
  vi.stubEnv('E2E_STATE_PATH', undefined)
  vi.stubEnv('LEAPMUX_E2E_NONCE_PATH', join(directory, 'nonce'))
  vi.stubEnv('LEAPMUX_E2E_NONCE', 'expected-nonce')
})

afterEach(() => {
  vi.unstubAllEnvs()
  rmSync(directory, { recursive: true, force: true })
})

describe('end-to-end global setup', () => {
  it('publishes state only in the authenticated run directory', async () => {
    writeFileSync(join(directory, 'nonce'), 'expected-nonce\n')
    await globalSetup()
    const path = join(directory, 'e2e-state.json')
    expect(process.env.E2E_STATE_PATH).toBe(path)
    const state = JSON.parse(readFileSync(path, 'utf8'))
    expect(state.tmpDir).toBe(directory)
    expect(state.binaryPath).toBe(resolve(import.meta.dirname, '../../..', process.platform === 'win32' ? 'leapmux.exe' : 'leapmux'))
  })

  it.each(['missing path', 'missing nonce', 'missing file', 'wrong nonce'])('rejects %s without publishing state', async (failure) => {
    if (failure !== 'missing file')
      writeFileSync(join(directory, 'nonce'), failure === 'wrong nonce' ? 'other-run' : 'expected-nonce')
    if (failure === 'missing path')
      vi.stubEnv('LEAPMUX_E2E_NONCE_PATH', undefined)
    if (failure === 'missing nonce')
      vi.stubEnv('LEAPMUX_E2E_NONCE', undefined)
    await expect(globalSetup()).rejects.toThrow('Run end-to-end tests with')
    expect(process.env.E2E_STATE_PATH).toBeUndefined()
    expect(existsSync(join(directory, 'e2e-state.json'))).toBe(false)
  })
})
