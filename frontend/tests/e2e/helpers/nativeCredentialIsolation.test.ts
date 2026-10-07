import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { assertIsolatedConfiguration, assertPrivateNativePath, exerciseCredentialIsolation } from './nativeCredentialIsolation'

const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
let scratch: string
let runDir: string

/** The run directory that the suite state gives, and the prompt of each native turn. */
const suite = vi.hoisted(() => ({ runDir: '', turn: [] as string[] }))

vi.mock('./server', async importOriginal => ({
  ...await importOriginal<typeof import('./server')>(),
  getGlobalState: () => ({ tmpDir: suite.runDir }),
}))

vi.mock('./nativeConversation', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeConversation')>(),
  sendNativeAnswer: async (_context: unknown, prompt: string): Promise<MockModelRequestRecord> => {
    suite.turn.push(prompt)
    return { protocol: 'anthropic-messages', path: '/v1/messages', stepIndex: 0, body: {}, mockCredential: { accepted: true } } as MockModelRequestRecord
  },
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return { ...actual, expect }
})

beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  scratch = mkdtempSync(join(scratchRoot, 'native-private-path-unit-'))
  runDir = join(scratch, 'private-run')
  mkdirSync(runDir)
  suite.runDir = runDir
  suite.turn = []
})

afterEach(() => rmSync(scratch, { recursive: true, force: true }))

describe('assertPrivateNativePath', () => {
  it('accepts actual private files and directories with spaces', () => {
    const directory = join(runDir, 'native configuration')
    mkdirSync(directory)
    const file = join(directory, 'models.json')
    writeFileSync(file, '{}')
    expect(() => assertPrivateNativePath(directory, runDir)).not.toThrow()
    expect(() => assertPrivateNativePath(file, runDir)).not.toThrow()
  })

  it('refuses a symlink inside the native run that resolves outside it', () => {
    const outside = join(scratch, 'outside-run')
    mkdirSync(outside)
    const link = join(runDir, 'native-home')
    symlinkSync(outside, link, 'junction')
    expect(() => assertPrivateNativePath(link, runDir)).toThrow('outside the E2E run')
  })

  it('resolves a directory link before the following parent segment', () => {
    const outside = join(scratch, 'outside-run')
    const child = join(outside, 'child')
    mkdirSync(child, { recursive: true })
    writeFileSync(join(outside, 'settings.json'), 'outside bytes')
    writeFileSync(join(runDir, 'settings.json'), 'private bytes')
    const link = join(runDir, 'linked-parent')
    symlinkSync(child, link, 'junction')
    const path = `${link}${sep}..${sep}settings.json`
    const actualText = readFileSync(path, 'utf8')
    if (actualText === 'outside bytes') {
      expect(() => assertPrivateNativePath(path, runDir)).toThrow('outside the E2E run')
    }
    else {
      expect(actualText).toBe('private bytes')
      expect(() => assertPrivateNativePath(path, runDir)).not.toThrow()
    }
  })

  it('refuses a lexical path outside the private run', () => {
    expect(() => assertPrivateNativePath(scratch, runDir)).toThrow('outside the E2E run')
  })

  it.each([{ path: '', run: 'run' }, { path: 'path', run: '' }])('refuses an empty private path or run directory: %j', ({ path, run }) => {
    expect(() => assertPrivateNativePath(path, run)).toThrow('must be nonempty')
  })

  it('states a private path that does not exist, in place of a raw lstat error', () => {
    const absent = join(runDir, 'agent-home', '.claude')
    expect(() => assertPrivateNativePath(absent, runDir)).toThrow(`The private native path ${absent} does not exist.`)
  })

  it('states a run directory that does not exist', () => {
    const absentRun = join(scratch, 'absent-run')
    expect(() => assertPrivateNativePath(runDir, absentRun)).toThrow(`The E2E run directory ${absentRun} does not exist.`)
  })

  it('states a link whose target does not exist', () => {
    const link = join(runDir, 'dangling')
    symlinkSync(join(runDir, 'absent-target'), link, 'junction')
    expect(() => assertPrivateNativePath(link, runDir)).toThrow(`The private native path ${link} does not exist.`)
  })
})

describe('assertIsolatedConfiguration', () => {
  const origin = 'http://127.0.0.1:4100'
  const key = 'mock-key-123'

  it('accepts a configuration that points at the mock and states the credential and each marker', () => {
    const configuration = `base_url = "${origin}/v1"\napi_key = "${key}"\nprovider = "mock"`
    expect(() => assertIsolatedConfiguration(configuration, { mockOrigin: origin, expectedCredential: key, configurationMarkers: ['provider = "mock"'] })).not.toThrow()
  })

  it.each([
    ['no mock origin', 'api_key = "mock-key-123"', { expectedCredential: key }, 'does not point at the suite mock'],
    ['no credential', `base_url = "${origin}"`, { expectedCredential: key }, 'does not state the expected credential'],
    ['no marker', `base_url = "${origin}"\napi_key = "${key}"`, { expectedCredential: key, configurationMarkers: ['provider = "mock"'] }, 'lacks the marker'],
  ])('refuses a configuration with %s', (_name, configuration, rules, message) => {
    expect(() => assertIsolatedConfiguration(configuration, { mockOrigin: origin, ...rules })).toThrow(message)
  })

  // A CLI that reads its key from an environment variable keeps no key in the file. The test of such a
  // CLI states that the file holds none, and the accepted mock credential of the turn proves the key.
  it('accepts a configuration that specifies an environment variable and holds no key', () => {
    const configuration = `base_url = "${origin}"\napi_key_env = "LEAPMUX_E2E_MODEL_API_KEY"`
    expect(() => assertIsolatedConfiguration(configuration, { mockOrigin: origin, absentFromConfiguration: [key], configurationMarkers: ['api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"'] })).not.toThrow()
  })

  it('refuses a configuration that holds a key that it must not hold', () => {
    const configuration = `base_url = "${origin}"\napi_key_env = "LEAPMUX_E2E_MODEL_API_KEY"\napi_key = "${key}"`
    expect(() => assertIsolatedConfiguration(configuration, { mockOrigin: origin, absentFromConfiguration: [key] })).toThrow('holds text that it must not hold')
  })

  it.each([
    ['no statement about the credential', {}, 'must state its credential'],
    ['an empty expected credential', { expectedCredential: '' }, 'must be nonempty'],
    ['an empty text that must be absent', { absentFromConfiguration: [''] }, 'must be nonempty'],
  ])('refuses rules with %s', (_name, rules, message) => {
    expect(() => assertIsolatedConfiguration(`base_url = "${origin}"`, { mockOrigin: origin, ...rules })).toThrow(message)
  })
})

describe('exerciseCredentialIsolation', () => {
  // An unset environment variable reaches the inline configuration as undefined.
  // The check refuses it before any environment read or browser access.
  it.each([[['']], [[undefined]], [['http://127.0.0.1:4100', undefined]]])('refuses the inline configuration %j before any other step', async (inlineConfiguration) => {
    const context = {
      provider: AgentProvider.CODEX,
      workspaceId: 'credential-boundary',
      get leapmuxServer(): never {
        throw new Error('The inline configuration check must run before the environment check.')
      },
      get page(): Page {
        throw new Error('The inline configuration check must run before browser access.')
      },
      get modelScript(): ModelScript {
        throw new Error('The inline configuration check must run before model access.')
      },
    }
    await expect(Reflect.apply(exerciseCredentialIsolation, undefined, [context, { inlineConfiguration, privateDirectories: [runDir], expectedCredential: 'mock-key-123' }])).rejects.toThrow('Each inline native configuration must be a nonempty string.')
  })
})

describe('exerciseCredentialIsolation with private directories', () => {
  const origin = 'http://127.0.0.1:4100'
  const key = 'mock-key-123'

  /** A context whose isolated HOME is inside the run. The mocked turn never touches the page or the model script. */
  function scenario(): { context: ManagedNativeScenarioContext, home: string } {
    const home = join(runDir, 'agent-home')
    mkdirSync(home)
    const context: ManagedNativeScenarioContext = {
      provider: AgentProvider.CLAUDE_CODE,
      providerAgent: { provider: AgentProvider.CLAUDE_CODE, prefix: 'native-e2e' },
      workspaceId: 'credential-workspace',
      page: {} as Page,
      modelScript: {} as ModelScript,
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused', agentEnv: { HOME: home }, mockModelUrl: `${origin}/mock` },
    }
    return { context, home }
  }

  it('runs the native turn after every private directory proves private', async () => {
    const { context, home } = scenario()
    const config = join(home, '.claude')
    mkdirSync(config)
    await exerciseCredentialIsolation(context, { privateDirectories: [home, config], inlineConfiguration: [origin, key], expectedCredential: key })
    expect(suite.turn).toHaveLength(1)
  })

  // The first spec of a shard runs before any CLI writes its own directory. Such a spec failed with a raw lstat error.
  it('states a private directory that does not exist before the turn, and runs no turn', async () => {
    const { context, home } = scenario()
    const absent = join(home, '.claude')
    await expect(exerciseCredentialIsolation(context, { privateDirectories: [home, absent], inlineConfiguration: [origin, key], expectedCredential: key }))
      .rejects
      .toThrow(`The private directory ${absent} does not exist before the native turn.`)
    expect(suite.turn).toEqual([])
  })

  it('refuses a private directory outside the run, and runs no turn', async () => {
    const { context, home } = scenario()
    const outside = join(scratch, 'outside-run')
    mkdirSync(outside)
    await expect(exerciseCredentialIsolation(context, { privateDirectories: [home, outside], inlineConfiguration: [origin, key], expectedCredential: key }))
      .rejects
      .toThrow('outside the E2E run')
    expect(suite.turn).toEqual([])
  })

  it.each([[['']], [[undefined]]])('refuses the private directories %j before any other step', async (privateDirectories) => {
    const { context } = scenario()
    await expect(Reflect.apply(exerciseCredentialIsolation, undefined, [context, { privateDirectories, inlineConfiguration: [origin, key], expectedCredential: key }]))
      .rejects
      .toThrow('Each private directory must be a nonempty path.')
    expect(suite.turn).toEqual([])
  })

  it('states an isolated HOME that does not exist', async () => {
    const { context } = scenario()
    const absentHome = join(runDir, 'absent-home')
    const server = { ...context.leapmuxServer, agentEnv: { HOME: absentHome } }
    await expect(exerciseCredentialIsolation({ ...context, leapmuxServer: server }, { privateDirectories: [runDir], inlineConfiguration: [origin, key], expectedCredential: key }))
      .rejects
      .toThrow(`The isolated native HOME ${absentHome} does not exist.`)
  })
})
