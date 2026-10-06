import type { MessageInitShape } from '@bufbuild/protobuf'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { LETTA_MODE } from '../../../src/generated/contracts/letta-protocol'
import { AgentInfoSchema, AgentProvider, AvailableOptionGroupSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openAgentViaAPI } from '../helpers/api'
import { requireBinary } from '../helpers/binaryOnPath'
import { createMockAgentEnvironment } from '../helpers/mockAgentEnvironment'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { cleanupRegisteredLettaMcp, openMcpLettaAgent, prepareMcpLetta } from './fixtures'
import { LETTA_AGENT } from './scenarios'

const fixtures = vi.hoisted(() => new Map<string, unknown>())
vi.mock('../letta-fixtures', () => ({
  lettaTest: { extend: (definitions: Record<string, unknown>) => {
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

let directory: string
beforeEach(() => {
  const scratch = resolve(process.cwd(), '../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'letta-mcp-fixture-'))
  vi.mocked(createMockAgentEnvironment).mockReset()
  vi.mocked(withPrivateNativeWorkspace).mockReset()
  vi.mocked(requireBinary).mockReset().mockReturnValue(process.execPath)
  vi.mocked(openAgentViaAPI).mockReset().mockResolvedValue('controlled-native-agent')
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('openMcpLettaAgent', () => {
  it('opens the actual initial MCP agent in native Unrestricted mode', async () => {
    const server = { hubUrl: 'http://private-hub.test', adminToken: 'private-token', workerId: 'private-worker' }
    await openMcpLettaAgent(server, 'private-workspace', directory)
    expect(vi.mocked(openAgentViaAPI).mock.calls[0]?.[3]?.optionValues?.permissionMode).toBe(LETTA_MODE.Unrestricted)
    expect(vi.mocked(openAgentViaAPI).mock.calls[0]?.[3]).not.toHaveProperty('agentSessionId')
  })

  it('reopens the same actual MCP conversation in native Unrestricted mode', async () => {
    const server = { hubUrl: 'http://private-hub.test', adminToken: 'private-token', workerId: 'private-worker' }
    await openMcpLettaAgent(server, 'private-workspace', directory, 'actual-native-conversation')
    expect(vi.mocked(openAgentViaAPI).mock.calls[0]?.[3]?.agentSessionId).toBe('actual-native-conversation')
    expect(vi.mocked(openAgentViaAPI).mock.calls[0]?.[3]?.optionValues?.permissionMode).toBe(LETTA_MODE.Unrestricted)
  })
})

describe('cleanupRegisteredLettaMcp', () => {
  it('closes the resumed agent before restoration', async () => {
    const events: string[] = []
    await cleanupRegisteredLettaMcp({
      agentId: 'resumed-native-agent',
      close: async (id) => { events.push(`close:${id}`) },
      restore: () => { events.push('restore') },
    })
    expect(events).toEqual(['close:resumed-native-agent', 'restore'])
  })

  it('restores the native registration when close fails and retains the close error', async () => {
    const path = join(directory, 'registration.json')
    writeFileSync(path, 'configured native registration')
    const closed = new Error('The controlled Worker close failed.')
    const close = vi.fn(async () => {
      throw closed
    })
    const restore = vi.fn(() => writeFileSync(path, 'original native registration'))
    await expect(cleanupRegisteredLettaMcp({ agentId: 'resumed-native-agent', close, restore })).rejects.toBe(closed)
    expect(close).toHaveBeenCalledWith('resumed-native-agent')
    expect(restore).toHaveBeenCalledTimes(1)
    expect(readFileSync(path, 'utf8')).toBe('original native registration')
  })

  it('retains both close and restoration failures', async () => {
    const closed = new Error('The controlled Worker close failed.')
    const restored = new Error('The controlled native restoration failed.')
    const close = vi.fn(async () => {
      throw closed
    })
    const restore = vi.fn(() => {
      throw restored
    })
    await expect(cleanupRegisteredLettaMcp({ agentId: 'resumed-native-agent', close, restore })).rejects.toMatchObject({ errors: [closed, restored] })
    expect(restore).toHaveBeenCalledTimes(1)
  })

  it('restores registration when native reopen created no agent', async () => {
    const close = vi.fn(async () => {})
    const restore = vi.fn()
    await cleanupRegisteredLettaMcp({ agentId: '', close, restore })
    expect(close).not.toHaveBeenCalled()
    expect(restore).toHaveBeenCalledTimes(1)
  })
})

describe('prepareMcpLetta', () => {
  it('builds a private mock environment with a local backend in the run directory, and keeps its paths', async () => {
    vi.mocked(createMockAgentEnvironment).mockResolvedValue({ homeDir: join(directory, 'home'), piAgentDir: directory, ohMyPiAgentDir: directory, env: { HOME: join(directory, 'home'), LETTA_LOCAL_BACKEND_DIR: join(directory, 'backend') } })
    const prepared = await prepareMcpLetta(directory, 'http://127.0.0.1:1')
    expect(createMockAgentEnvironment).toHaveBeenCalledExactlyOnceWith(directory, 'http://127.0.0.1:1')
    expect(prepared).toEqual({
      agentEnv: { HOME: join(directory, 'home'), LETTA_LOCAL_BACKEND_DIR: join(directory, 'backend') },
      setup: { home: join(directory, 'home'), backendDirectory: join(directory, 'backend'), nodeExecutable: process.execPath },
    })
  })

  it('refuses an environment with no local backend directory', async () => {
    vi.mocked(createMockAgentEnvironment).mockResolvedValue({ homeDir: directory, piAgentDir: directory, ohMyPiAgentDir: directory, env: { HOME: directory } })
    await expect(prepareMcpLetta(directory, 'http://127.0.0.1:1')).rejects.toThrow('requires a local backend directory')
  })

  it('keeps the failure of the environment construction', async () => {
    const constructed = new Error('The controlled private environment failed.')
    vi.mocked(createMockAgentEnvironment).mockRejectedValue(constructed)
    await expect(prepareMcpLetta(directory, 'http://127.0.0.1:1')).rejects.toBe(constructed)
  })
})

describe('privateMcpLettaWorkspace', () => {
  const leapmuxServer = { hubUrl: 'http://private-hub.test', adminToken: 'private-token', workerId: 'suite-worker', agentEnv: { HOME: '/suite/home' }, mockModelUrl: 'http://127.0.0.1:1' }
  const privateServer = { ...leapmuxServer, workerId: 'private-worker' }
  const setup = { home: '/private/home', backendDirectory: '/private/backend', nodeExecutable: process.execPath }

  function permissionMode(currentValue: string) {
    return create(AvailableOptionGroupSchema, { id: OPTION_ID_PERMISSION_MODE, currentValue })
  }

  /** Run the fixture with the private workspace that the skeleton hands over after its startup, with `agent`. */
  async function runFixture(agent: MessageInitShape<typeof AgentInfoSchema>, use: (value: unknown) => Promise<void>) {
    vi.mocked(withPrivateNativeWorkspace).mockImplementation(async (_page, _server, options, useWorkspace) => {
      expect(options).toMatchObject({ prefix: 'letta-mcp', workerName: 'Letta MCP test', providerAgent: LETTA_AGENT, openAgent: openMcpLettaAgent })
      await (useWorkspace as (value: unknown) => Promise<void>)({
        workspaceId: 'private-workspace',
        server: privateServer,
        agentId: 'letta-agent',
        workingDir: '/private/wd',
        agent: create(AgentInfoSchema, { id: 'letta-agent', agentProvider: AgentProvider.LETTA, agentSessionId: 'native-conversation', ...agent }),
        runDirectory: directory,
        setup,
      })
    })
    const fixture = fixtures.get('privateMcpLettaWorkspace')
    if (typeof fixture !== 'function')
      throw new Error('The private Letta MCP fixture definition is absent.')
    await fixture({ page: {}, leapmuxServer }, use)
  }

  it('yields the workspace of a started Letta agent in native Unrestricted mode', async () => {
    const use = vi.fn(async () => {})
    await runFixture({ optionGroups: [permissionMode(LETTA_MODE.Unrestricted)] }, use)
    expect(use).toHaveBeenCalledExactlyOnceWith({ workspaceId: 'private-workspace', server: privateServer, runDirectory: directory, workingDir: '/private/wd', ...setup })
  })

  it.each([
    ['another provider', { agentProvider: AgentProvider.CLAUDE_CODE }],
    ['no native conversation', { agentSessionId: '' }],
    ['another permission mode', { optionGroups: [permissionMode('default')] }],
  ])('does not yield the workspace of an agent with %s', async (_case, agent) => {
    const use = vi.fn(async () => {})
    await expect(runFixture({ optionGroups: [permissionMode(LETTA_MODE.Unrestricted)], ...agent }, use)).rejects.toThrow()
    expect(use).not.toHaveBeenCalled()
  })
})
