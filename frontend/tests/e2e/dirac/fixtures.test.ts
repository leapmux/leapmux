import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openAgentViaAPI } from '../helpers/api'
import { requireBinary } from '../helpers/binaryOnPath'
import { createMockAgentEnvironment } from '../helpers/mockAgentEnvironment'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { createServerOutput } from '../helpers/serverOutput'
import { loginViaToken, openWorkspace } from '../helpers/ui'
import { withTestWorkspace } from '../helpers/workspace'
import { readDiracMcpSessionObservation } from './mcpConfiguration'
import './fixtures'

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
vi.mock('../helpers/nativeWorker', () => ({ withNativeWorker: vi.fn() }))
vi.mock('../helpers/runDirectory', () => ({ createTestDirectory: vi.fn() }))
vi.mock('../helpers/binaryOnPath', async original => ({
  ...await original<typeof import('../helpers/binaryOnPath')>(),
  requireBinary: vi.fn(),
}))
vi.mock('../helpers/api', async original => ({
  ...await original<typeof import('../helpers/api')>(),
  openAgentViaAPI: vi.fn(),
}))
vi.mock('../helpers/nativeScenario', async original => ({
  ...await original<typeof import('../helpers/nativeScenario')>(),
  currentNativeAgent: vi.fn(),
}))
vi.mock('../helpers/ui', async original => ({
  ...await original<typeof import('../helpers/ui')>(),
  loginViaToken: vi.fn(async () => {}),
  openWorkspace: vi.fn(async () => {}),
}))
vi.mock('../helpers/workspace', async original => ({
  ...await original<typeof import('../helpers/workspace')>(),
  withTestWorkspace: vi.fn(),
}))
vi.mock('./mcpConfiguration', async original => ({
  ...await original<typeof import('./mcpConfiguration')>(),
  readDiracMcpSessionObservation: vi.fn(),
}))

let directory: string
beforeEach(() => {
  const scratch = resolve(process.cwd(), '../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'dirac-mcp-fixture-'))
  vi.mocked(createTestDirectory).mockReset().mockReturnValue(directory)
  vi.mocked(createMockAgentEnvironment).mockReset()
  vi.mocked(withNativeWorker).mockReset()
  vi.mocked(requireBinary).mockReset().mockReturnValue(process.execPath)
  vi.mocked(openAgentViaAPI).mockReset().mockResolvedValue('controlled-dirac-agent')
  vi.mocked(currentNativeAgent).mockReset()
  vi.mocked(readDiracMcpSessionObservation).mockReset()
  vi.mocked(loginViaToken).mockClear()
  vi.mocked(openWorkspace).mockClear()
  vi.mocked(withTestWorkspace).mockReset().mockImplementation(async (_server, _prefix, use) => use({ workspaceId: 'controlled-workspace' }))
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('configuredMcpDiracWorkspace', () => {
  function prepareReachedWorker() {
    vi.mocked(createMockAgentEnvironment).mockResolvedValue({
      homeDir: directory,
      piAgentDir: join(directory, 'private-pi'),
      ohMyPiAgentDir: join(directory, 'private-ohmypi'),
      env: { HOME: directory, PATH: 'controlled-private-path' },
    })
    vi.mocked(withNativeWorker).mockImplementation(async (server, _options, use) => {
      await use({ server: { ...server, workerId: 'controlled-worker', agentEnv: server.agentEnv ?? {} }, workerId: 'controlled-worker', dataDir: directory, output: createServerOutput() })
    })
    const fixture = fixtures.get('configuredMcpDiracWorkspace')
    if (typeof fixture !== 'function')
      throw new Error('The private Dirac MCP fixture definition is absent.')
    return fixture
  }

  it('holds the actual use callback until native startup and the paired session receipt', async () => {
    const fixture = prepareReachedWorker()
    let enterStartup!: (value: 'startup') => void
    let enterUse!: (value: 'use') => void
    let completeStartup!: (value: AgentInfo) => void
    const startupEntered = new Promise<'startup'>(resolve => enterStartup = resolve)
    const useEntered = new Promise<'use'>(resolve => enterUse = resolve)
    const startup = new Promise<AgentInfo>(resolve => completeStartup = resolve)
    const agent = create(AgentInfoSchema, { id: 'controlled-dirac-agent', agentProvider: AgentProvider.DIRAC, status: AgentStatus.ACTIVE, agentSessionId: 'controlled-native-session', workingDir: directory })
    vi.mocked(currentNativeAgent).mockImplementation(() => {
      enterStartup('startup')
      return startup
    })
    vi.mocked(readDiracMcpSessionObservation).mockReturnValue({
      request: { jsonrpc: '2.0', id: 0, method: 'session/new', params: { cwd: directory, mcpServers: [{ name: 'form_probe', command: process.execPath, args: [join(directory, 'configured-form.mjs')], env: [] }] } },
      reply: { jsonrpc: '2.0', id: 0, result: { sessionId: agent.agentSessionId } },
      sessionId: agent.agentSessionId,
    })
    const use = vi.fn(async () => {
      enterUse('use')
    })
    const running = fixture({ page: {}, leapmuxServer: { mockModelUrl: 'http://127.0.0.1:1', hubUrl: 'http://private-hub.test', adminToken: 'private-token' } }, use)
    try {
      expect(await Promise.race([startupEntered, useEntered])).toBe('startup')
      expect(use).not.toHaveBeenCalled()
      expect(readDiracMcpSessionObservation).not.toHaveBeenCalled()
    }
    finally {
      completeStartup(agent)
      await running
    }
    expect(readDiracMcpSessionObservation).toHaveBeenCalledTimes(1)
    expect(use).toHaveBeenCalledTimes(1)
  })

  it('preserves a native startup failure without yielding the actual fixture or discarding Worker files', async () => {
    const fixture = prepareReachedWorker()
    const failed = new Error('The controlled native Dirac startup failed.')
    vi.mocked(currentNativeAgent).mockRejectedValue(failed)
    const use = vi.fn(async () => {})
    await expect(fixture({ page: {}, leapmuxServer: { mockModelUrl: 'http://127.0.0.1:1', hubUrl: 'http://private-hub.test', adminToken: 'private-token' } }, use)).rejects.toBe(failed)
    expect(use).not.toHaveBeenCalled()
    expect(readDiracMcpSessionObservation).not.toHaveBeenCalled()
    expect(existsSync(directory)).toBe(true)
  })

  it('removes partial configuration when environment construction fails before any Worker exists', async () => {
    const constructed = new Error('The controlled private Dirac environment failed.')
    vi.mocked(createMockAgentEnvironment).mockImplementation(async () => {
      writeFileSync(join(directory, 'partial-configuration.json'), '{}')
      throw constructed
    })
    const fixture = fixtures.get('configuredMcpDiracWorkspace')
    if (typeof fixture !== 'function')
      throw new Error('The private Dirac MCP fixture definition is absent.')
    await expect(fixture({ page: {}, leapmuxServer: { mockModelUrl: 'http://127.0.0.1:1' } }, async () => {})).rejects.toBe(constructed)
    expect(withNativeWorker).not.toHaveBeenCalled()
    expect(existsSync(directory)).toBe(false)
  })

  it('preserves private files after a Worker attempt without a physical stop receipt', async () => {
    const attempted = new Error('The controlled Dirac Worker attempt failed without a stop receipt.')
    vi.mocked(createMockAgentEnvironment).mockResolvedValue({
      homeDir: directory,
      piAgentDir: join(directory, 'private-pi'),
      ohMyPiAgentDir: join(directory, 'private-ohmypi'),
      env: { HOME: directory, PATH: 'controlled-private-path' },
    })
    vi.mocked(withNativeWorker).mockImplementation(async () => {
      writeFileSync(join(directory, 'attempted-worker.txt'), 'preserve these live Dirac Worker files')
      throw attempted
    })
    const fixture = fixtures.get('configuredMcpDiracWorkspace')
    if (typeof fixture !== 'function')
      throw new Error('The private Dirac MCP fixture definition is absent.')
    await expect(fixture({ page: {}, leapmuxServer: { mockModelUrl: 'http://127.0.0.1:1' } }, async () => {})).rejects.toBe(attempted)
    expect(withNativeWorker).toHaveBeenCalledTimes(1)
    expect(existsSync(join(directory, 'attempted-worker.txt'))).toBe(true)
  })
})
