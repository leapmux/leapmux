import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import globalSetup from './global-setup'

const suiteServer = vi.hoisted(() => ({
  start: vi.fn(),
  stop: vi.fn(),
}))

vi.mock('./helpers/suiteServer', () => ({ startSuiteServer: suiteServer.start }))

let directory: string

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'global-setup-test-'))
  vi.stubEnv('E2E_STATE_PATH', undefined)
  vi.stubEnv('LEAPMUX_E2E_NONCE_PATH', join(directory, 'nonce'))
  vi.stubEnv('LEAPMUX_E2E_NONCE', 'expected-nonce')
  suiteServer.stop.mockResolvedValue(undefined)
  suiteServer.start.mockResolvedValue({
    state: {
      hubUrl: 'http://localhost:1234',
      adminToken: 'admin-token',
      adminUserId: 'admin-user',
      workerId: 'worker',
      newuserToken: 'new-user-token',
      dataDir: join(directory, 'server-data'),
      serverLogPath: join(directory, 'server.log'),
      mockModelUrl: 'http://127.0.0.1:5678',
      piAgentDir: join(directory, 'pi-agent'),
    },
    stop: suiteServer.stop,
  })
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.clearAllMocks()
  rmSync(directory, { recursive: true, force: true })
})

describe('end-to-end global setup', () => {
  it('publishes state only in the authenticated run directory', async () => {
    writeFileSync(join(directory, 'nonce'), 'expected-nonce\n')
    const teardown = await globalSetup()
    const path = join(directory, 'e2e-state.json')
    expect(process.env.E2E_STATE_PATH).toBe(path)
    const state = JSON.parse(readFileSync(path, 'utf8'))
    expect(state.tmpDir).toBe(directory)
    expect(state.binaryPath).toBe(resolve(import.meta.dirname, '../../..', process.platform === 'win32' ? 'leapmux.exe' : 'leapmux'))
    expect(state).toMatchObject({
      hubUrl: 'http://localhost:1234',
      workerId: 'worker',
      mockModelUrl: 'http://127.0.0.1:5678',
    })
    expect(process.env.PI_CODING_AGENT_DIR).toBe(state.piAgentDir)
    expect(suiteServer.start).toHaveBeenCalledWith({ binaryPath: state.binaryPath, tmpDir: directory })
    await teardown?.()
    expect(suiteServer.stop).toHaveBeenCalledOnce()
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
    expect(suiteServer.start).not.toHaveBeenCalled()
  })

  it('does not publish partial state when shared server startup fails', async () => {
    writeFileSync(join(directory, 'nonce'), 'expected-nonce\n')
    suiteServer.start.mockRejectedValueOnce(new Error('server startup failed'))

    await expect(globalSetup()).rejects.toThrow('server startup failed')
    expect(process.env.E2E_STATE_PATH).toBeUndefined()
    expect(existsSync(join(directory, 'e2e-state.json'))).toBe(false)
  })
})
