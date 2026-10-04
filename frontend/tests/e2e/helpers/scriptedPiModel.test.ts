import type { ExecFileOptions } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MODEL_KEY } from './mockAgentEnvironment'
import { withMockPiModel } from './scriptedPiModel'

const state = vi.hoisted(() => ({ execFile: vi.fn(), runDir: '' }))
vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  return { ...actual, execFile: state.execFile }
})
vi.mock('./server', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./server')>()
  return { ...actual, getGlobalState: () => ({ tmpDir: state.runDir }) }
})

let directory: string
let nativeEnvironment: { HOME: string, PI_CODING_AGENT_DIR: string, OPENAI_API_KEY: string }
let failEnable: Error | undefined
let failRestore: Error | undefined
const executions: Array<{ binary: string, args: readonly string[], options: ExecFileOptions }> = []
let outsideDirectory: string | undefined

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  state.runDir = mkdtempSync(join(scratch, 'pi-model-test-'))
  directory = join(state.runDir, 'project')
  nativeEnvironment = { HOME: join(state.runDir, 'home'), PI_CODING_AGENT_DIR: join(state.runDir, 'pi-agent'), OPENAI_API_KEY: MODEL_KEY }
  for (const path of [directory, nativeEnvironment.HOME, nativeEnvironment.PI_CODING_AGENT_DIR])
    mkdirSync(path, { recursive: true })
  executions.length = 0
  failEnable = undefined
  failRestore = undefined
  state.execFile.mockReset()
  vi.stubEnv('E2E_STATE_PATH', '')
  vi.stubEnv('PI_CODING_AGENT_DIR', 'inherited-user-profile-must-not-reach-cli')
  state.execFile.mockImplementation((binary: string, args: readonly string[], options: ExecFileOptions, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
    executions.push({ binary, args, options })
    const enable = args.at(-1)?.endsWith('protocol-trust-enable.ts')
    if (enable)
      writeFileSync(join(directory, 'prior-project-trust.json'), 'null')
    const failure = enable ? failEnable : failRestore
    if (!failure)
      writeFileSync(join(directory, enable ? 'trust-enabled' : 'trust-restored'), 'ready')
    callback(failure ?? null, '', '')
  })
})

afterEach(() => {
  rmSync(state.runDir, { recursive: true, force: true })
  if (outsideDirectory)
    rmSync(outsideDirectory, { recursive: true, force: true })
  outsideDirectory = undefined
  vi.unstubAllEnvs()
})

function server(mockModelUrl = 'http://127.0.0.1:1234') {
  return { mockModelUrl, agentEnv: nativeEnvironment }
}

describe('withMockPiModel', () => {
  it.each([
    'https://127.0.0.1:1234',
    'http://example.com:1234',
    'http://127.0.0.1:1234/prefix',
  ])('refuses a non-local mock server before it writes a Pi extension: %s', async (url) => {
    await expect(withMockPiModel(directory, server(url), async () => {})).rejects.toThrow('loopback HTTP origin')
    expect(existsSync(join(directory, '.pi'))).toBe(false)
    expect(executions).toEqual([])
  })

  it('passes the same isolated profile to both trust subprocesses and uses the canonical mock credential', async () => {
    await withMockPiModel(directory, server(), async (settings) => {
      expect(settings).toEqual({ model: 'probe', optionValues: { pi_provider: 'leapmux-control-test', effort: 'off' } })
      const extension = readFileSync(join(directory, '.pi', 'extensions', 'protocol-model.ts'), 'utf8')
      expect(extension).toContain('http://127.0.0.1:1234/v1')
      expect(extension).toContain(MODEL_KEY)
      expect(executions).toHaveLength(1)
    })
    expect(executions).toHaveLength(2)
    for (const execution of executions) {
      expect(execution.binary).toBe('pi')
      expect(execution.options.cwd).toBe(directory)
      expect(execution.options.env).toMatchObject(nativeEnvironment)
      expect(execution.options.env?.PI_CODING_AGENT_DIR).not.toBe('inherited-user-profile-must-not-reach-cli')
      expect(execution.options.env?.HTTP_PROXY).toBeUndefined()
    }
    expect(executions[0]?.options.env).toBe(executions[1]?.options.env)
    expect(executions[1]?.args.at(-1)).toBe(join(directory, 'protocol-trust-restore.ts'))
  })

  it('loads the current native Pi package for both trust operations', async () => {
    await withMockPiModel(directory, server(), async () => {})
    for (const execution of executions) {
      const script = readFileSync(execution.args.at(-1)!, 'utf8')
      expect(script).toContain('from \'@earendil-works/pi-coding-agent\'')
      expect(script).not.toContain('@mariozechner/pi-coding-agent')
    }
  })

  it('restores native trust after the test body fails and preserves the original error', async () => {
    const failure = new Error('The native browser proof failed.')
    await expect(withMockPiModel(directory, server(), async () => {
      throw failure
    })).rejects.toBe(failure)
    expect(executions).toHaveLength(2)
  })

  it('retains the body failure and the native restore failure', async () => {
    const bodyFailure = new Error('The native browser proof failed.')
    failRestore = new Error('The native trust restore failed.')
    await expect(withMockPiModel(directory, server(), async () => {
      throw bodyFailure
    })).rejects.toMatchObject({ errors: [bodyFailure, failRestore] })
  })

  it('restores the prior entry when native trust enable fails after saving that entry', async () => {
    failEnable = new Error('The native trust enable failed after recording the prior entry.')
    const run = vi.fn(async () => {})
    await expect(withMockPiModel(directory, server(), run)).rejects.toBe(failEnable)
    expect(run).not.toHaveBeenCalled()
    expect(executions).toHaveLength(2)
    expect(executions[1]?.args.at(-1)).toBe(join(directory, 'protocol-trust-restore.ts'))
  })

  it('retains native enable and restore failures after a partial enable', async () => {
    failEnable = new Error('The native trust enable failed after recording the prior entry.')
    failRestore = new Error('The native trust restore failed.')
    await expect(withMockPiModel(directory, server(), async () => {})).rejects.toMatchObject({ errors: [failEnable, failRestore] })
    expect(executions).toHaveLength(2)
  })

  it('refuses a private profile symlink that resolves outside the run before it changes trust', async () => {
    outsideDirectory = mkdtempSync(join(resolve(import.meta.dirname, '../../../../.tmp'), 'pi-outside-profile-'))
    const linked = join(state.runDir, 'linked-pi-agent')
    symlinkSync(outsideDirectory, linked, 'junction')
    await expect(withMockPiModel(directory, { ...server(), agentEnv: { ...nativeEnvironment, PI_CODING_AGENT_DIR: linked } }, async () => {})).rejects.toThrow('outside the E2E run')
    expect(executions).toEqual([])
    expect(existsSync(join(directory, '.pi'))).toBe(false)
  })

  it.each([{ mockModelUrl: '' }, { mockModelUrl: 'http://127.0.0.1:1234', agentEnv: {} }])('refuses missing suite isolation before it changes trust: %j', async (context) => {
    await expect(Reflect.apply(withMockPiModel, undefined, [directory, context, async () => {}])).rejects.toThrow(/suite server URL|isolated native environment/)
    expect(executions).toEqual([])
    expect(existsSync(join(directory, '.pi'))).toBe(false)
  })
})
