import type { PrivateNativeWorkspace, PrivateNativeWorkspaceOptions, PrivateWorkerHub } from '../helpers/privateNativeWorkspace'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { requireBinary } from '../helpers/binaryOnPath'
import { createMockAgentEnvironment, MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { prepareAnthropicDirac, prepareMcpDirac } from './fixtures'
import { readDiracMcpSessionObservation } from './mcpConfiguration'
import { DIRAC_AGENT } from './scenarios'

const fixtures = vi.hoisted(() => new Map<string, unknown>())
vi.mock('../dirac-fixtures', () => ({
  diracTest: { extend: (definitions: Record<string, unknown>) => {
    for (const [name, definition] of Object.entries(definitions))
      fixtures.set(name, definition)
    return {}
  } },
}))
vi.mock('../helpers/mockAgentEnvironment', async original => ({
  ...await original<typeof import('../helpers/mockAgentEnvironment')>(),
  createMockAgentEnvironment: vi.fn(),
}))
vi.mock('../helpers/privateNativeWorkspace', () => ({ withPrivateNativeWorkspace: vi.fn() }))
vi.mock('../helpers/binaryOnPath', async original => ({
  ...await original<typeof import('../helpers/binaryOnPath')>(),
  requireBinary: vi.fn(),
}))
vi.mock('../helpers/api', async original => ({
  ...await original<typeof import('../helpers/api')>(),
  openAgentViaAPI: vi.fn(),
}))
vi.mock('./mcpConfiguration', async original => ({
  ...await original<typeof import('./mcpConfiguration')>(),
  readDiracMcpSessionObservation: vi.fn(),
}))

type AnyOptions = PrivateNativeWorkspaceOptions<PrivateWorkerHub, unknown>
type AnyWorkspace = PrivateNativeWorkspace<PrivateWorkerHub, unknown>

const leapmuxServer = { hubUrl: 'http://private-hub.test', adminToken: 'private-token', workerId: 'suite-worker', agentEnv: { HOME: '/suite/home' }, mockModelUrl: 'http://127.0.0.1:1' }
const privateServer = { ...leapmuxServer, workerId: 'private-worker' }
let directory: string

/** The fixture definition `name` that the fixture file registered. */
function fixture(name: string): (fixtures: object, use: (value: unknown) => Promise<void>) => Promise<void> {
  const definition = fixtures.get(name)
  if (typeof definition !== 'function')
    throw new Error(`The fixture ${name} is absent.`)
  return definition as (fixtures: object, use: (value: unknown) => Promise<void>) => Promise<void>
}

/** Run the private workspace as the skeleton does after its startup: hand `use` a workspace with `setup`. */
function runWorkspace(workspace: Partial<AnyWorkspace>): { options: () => AnyOptions } {
  let captured: AnyOptions | undefined
  vi.mocked(withPrivateNativeWorkspace).mockImplementation(async (_page, _server, options, use) => {
    captured = options as AnyOptions
    await (use as (value: AnyWorkspace) => Promise<void>)({
      workspaceId: 'private-workspace',
      server: privateServer,
      agentId: 'dirac-agent',
      workingDir: directory,
      agent: create(AgentInfoSchema, { id: 'dirac-agent', status: AgentStatus.ACTIVE, agentSessionId: 'native-session' }),
      runDirectory: directory,
      setup: undefined,
      ...workspace,
    })
  })
  return {
    options: () => {
      if (!captured)
        throw new Error('The fixture started no private workspace.')
      return captured
    },
  }
}

beforeEach(() => {
  const scratch = resolve(process.cwd(), '../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'dirac-private-fixture-'))
  vi.mocked(createMockAgentEnvironment).mockReset()
  vi.mocked(withPrivateNativeWorkspace).mockReset()
  vi.mocked(requireBinary).mockReset().mockReturnValue(process.execPath)
  vi.mocked(openAgentViaAPI).mockReset().mockResolvedValue('dirac-agent')
  vi.mocked(readDiracMcpSessionObservation).mockReset()
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('prepareAnthropicDirac', () => {
  it('writes the private Dirac home in the run directory and points Dirac at the mock', () => {
    const prepared = prepareAnthropicDirac(directory, 'http://127.0.0.1:7', 'claude-opus-5')
    const state = join(directory, 'dirac-home', 'data', 'globalState.json')
    expect(JSON.parse(readFileSync(state, 'utf8'))).toEqual({ telemetrySetting: 'disabled', autoApproveAllToggled: true, yoloModeToggled: true })
    expect(statSync(state).mode & 0o777).toBe(0o600)
    expect(prepared.agentEnv).toBeUndefined()
    expect(prepared.env).toEqual({
      DIRAC_DIR: join(directory, 'dirac-home'),
      DIRAC_PROVIDER: 'anthropic',
      DIRAC_API_KEY: MODEL_KEY,
      DIRAC_BASE_URL: 'http://127.0.0.1:7',
      DIRAC_MODEL: 'claude-opus-5',
      LEAPMUX_DIRAC_DEFAULT_MODEL: 'claude-opus-5',
    })
  })
})

describe('anthropicDiracWorkspace', () => {
  it('opens the Dirac agent with the Anthropic model on the private Worker, and hands its workspace to the test', async () => {
    const run = runWorkspace({})
    const use = vi.fn(async () => {})
    await fixture('anthropicDiracWorkspace')({ page: {}, leapmuxServer, anthropicModel: 'claude-opus-5' }, use)
    expect(run.options()).toMatchObject({ prefix: 'dirac-anthropic', workerName: 'Dirac Anthropic test', providerAgent: DIRAC_AGENT })
    await run.options().openAgent(privateServer, 'private-workspace', directory)
    expect(openAgentViaAPI).toHaveBeenCalledExactlyOnceWith('http://private-hub.test', 'private-token', 'private-worker', 'private-workspace', directory, {
      agentProvider: AgentProvider.DIRAC,
      model: 'claude-opus-5',
      optionValues: { permissionMode: 'act', reasoning_effort: 'medium' },
    })
    expect(use).toHaveBeenCalledExactlyOnceWith({ workspaceId: 'private-workspace', server: privateServer, agentId: 'dirac-agent', workingDir: directory })
  })
})

describe('prepareMcpDirac', () => {
  it('writes the form server and a wrapper first on PATH, and keeps the receipts and the configured server list', async () => {
    vi.mocked(createMockAgentEnvironment).mockResolvedValue({
      homeDir: directory,
      piAgentDir: join(directory, 'private-pi'),
      ohMyPiAgentDir: join(directory, 'private-ohmypi'),
      env: { HOME: directory, PATH: 'controlled-private-path' },
    })
    const prepared = await prepareMcpDirac(directory, 'http://127.0.0.1:7')
    expect(createMockAgentEnvironment).toHaveBeenCalledExactlyOnceWith(directory, 'http://127.0.0.1:7')
    expect(prepared.agentEnv).toEqual({ HOME: directory, PATH: 'controlled-private-path' })
    expect(prepared.env?.PATH?.startsWith(join(directory, 'wrapper'))).toBe(true)
    expect(prepared.env?.PATH?.endsWith('controlled-private-path')).toBe(true)
    expect(prepared.setup).toEqual({
      sessionReceipt: expect.stringContaining(join(directory, 'wrapper')),
      formReceipt: join(directory, 'configured-form-receipt.json'),
      configuredServers: [{ name: 'form_probe', command: process.execPath, args: [join(directory, 'configured-form.mjs')], env: [] }],
    })
    expect(existsSync(join(directory, 'configured-form.mjs'))).toBe(true)
  })

  it('keeps the failure of the environment construction and writes no wrapper', async () => {
    const constructed = new Error('The controlled private Dirac environment failed.')
    vi.mocked(createMockAgentEnvironment).mockRejectedValue(constructed)
    await expect(prepareMcpDirac(directory, 'http://127.0.0.1:7')).rejects.toBe(constructed)
    expect(existsSync(join(directory, 'wrapper'))).toBe(false)
  })
})

describe('configuredMcpDiracWorkspace', () => {
  const setup = {
    sessionReceipt: '/private/wrapper/native-mcp-session.jsonl',
    formReceipt: '/private/configured-form-receipt.json',
    configuredServers: [{ name: 'form_probe', command: process.execPath, args: ['/private/configured-form.mjs'], env: [] }],
  }

  function observation(cwd: string, sessionId = 'native-session') {
    return {
      request: { jsonrpc: '2.0' as const, id: 0, method: 'session/new', params: { cwd, mcpServers: setup.configuredServers } },
      reply: { jsonrpc: '2.0' as const, id: 0, result: { sessionId } },
      sessionId,
    }
  }

  it('proves the paired session receipt of the configured server list before it yields the workspace', async () => {
    const run = runWorkspace({ setup })
    vi.mocked(readDiracMcpSessionObservation).mockReturnValue(observation(directory))
    const use = vi.fn(async () => {})
    await fixture('configuredMcpDiracWorkspace')({ page: {}, leapmuxServer }, use)
    expect(readDiracMcpSessionObservation).toHaveBeenCalledExactlyOnceWith(setup.sessionReceipt)
    expect(use).toHaveBeenCalledExactlyOnceWith({ workspaceId: 'private-workspace', server: privateServer, workingDir: directory, agentId: 'dirac-agent', ...setup })
    expect(run.options()).toMatchObject({ prefix: 'dirac-mcp', workerName: 'Dirac MCP test', providerAgent: DIRAC_AGENT })
    await run.options().openAgent(privateServer, 'private-workspace', directory)
    expect(openAgentViaAPI).toHaveBeenCalledExactlyOnceWith('http://private-hub.test', 'private-token', 'private-worker', 'private-workspace', directory, agentOpenOptions(AgentProvider.DIRAC))
  })

  it.each([
    ['another working directory', () => observation('/elsewhere')],
    ['another native session', () => observation(directory, 'foreign-session')],
  ])('does not yield the workspace when the session receipt states %s', async (_case, observed) => {
    runWorkspace({ setup })
    vi.mocked(readDiracMcpSessionObservation).mockReturnValue(observed())
    const use = vi.fn(async () => {})
    await expect(fixture('configuredMcpDiracWorkspace')({ page: {}, leapmuxServer }, use)).rejects.toThrow()
    expect(use).not.toHaveBeenCalled()
  })
})
