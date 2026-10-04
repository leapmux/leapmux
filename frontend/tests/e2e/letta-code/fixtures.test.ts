import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LETTA_MODE } from '../../../src/generated/contracts/letta-protocol'
import { openAgentViaAPI } from '../helpers/api'
import { findBinary } from '../helpers/binaryOnPath'
import { createMockAgentEnvironment } from '../helpers/mockAgentEnvironment'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { cleanupRegisteredLettaMcp, openMcpLettaAgent } from './fixtures'

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
vi.mock('../helpers/nativeWorker', () => ({ withNativeWorker: vi.fn() }))
vi.mock('../helpers/runDirectory', () => ({ createTestDirectory: vi.fn() }))
vi.mock('../helpers/binaryOnPath', async original => ({
  ...await original<typeof import('../helpers/binaryOnPath')>(),
  findBinary: vi.fn(),
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
  vi.mocked(createTestDirectory).mockReset().mockReturnValue(directory)
  vi.mocked(createMockAgentEnvironment).mockReset()
  vi.mocked(withNativeWorker).mockReset()
  vi.mocked(findBinary).mockReset().mockReturnValue(process.execPath)
  vi.mocked(openAgentViaAPI).mockReset().mockResolvedValue('controlled-native-agent')
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('openMcpLettaAgent', () => {
  it('opens the actual initial MCP agent in native Unrestricted mode', async () => {
    const server = { hubUrl: 'http://private-hub.test', adminToken: 'private-token', workerId: 'private-worker' }
    await openMcpLettaAgent(server, 'private-workspace', directory)
    expect(vi.mocked(openAgentViaAPI).mock.calls[0]?.[5]?.optionValues?.permissionMode).toBe(LETTA_MODE.Unrestricted)
    expect(vi.mocked(openAgentViaAPI).mock.calls[0]?.[5]).not.toHaveProperty('agentSessionId')
  })

  it('reopens the same actual MCP conversation in native Unrestricted mode', async () => {
    const server = { hubUrl: 'http://private-hub.test', adminToken: 'private-token', workerId: 'private-worker' }
    await openMcpLettaAgent(server, 'private-workspace', directory, 'actual-native-conversation')
    expect(vi.mocked(openAgentViaAPI).mock.calls[0]?.[5]?.agentSessionId).toBe('actual-native-conversation')
    expect(vi.mocked(openAgentViaAPI).mock.calls[0]?.[5]?.optionValues?.permissionMode).toBe(LETTA_MODE.Unrestricted)
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

describe('privateMcpLettaWorkspace', () => {
  it('removes partial configuration when environment construction fails before any Worker exists', async () => {
    const constructed = new Error('The controlled private environment failed.')
    vi.mocked(createMockAgentEnvironment).mockImplementation(async () => {
      writeFileSync(join(directory, 'partial-configuration.json'), '{}')
      throw constructed
    })
    const fixture = fixtures.get('privateMcpLettaWorkspace')
    if (typeof fixture !== 'function')
      throw new Error('The private Letta MCP fixture definition is absent.')
    await expect(fixture({ page: {}, leapmuxServer: { mockModelUrl: 'http://127.0.0.1:1' } }, async () => {})).rejects.toBe(constructed)
    expect(withNativeWorker).not.toHaveBeenCalled()
    expect(existsSync(directory)).toBe(false)
  })

  it('preserves private files after a Worker attempt without a physical stop receipt', async () => {
    const attempted = new Error('The controlled Worker attempt failed without a stop receipt.')
    vi.mocked(createMockAgentEnvironment).mockResolvedValue({ homeDir: directory, piAgentDir: directory, ohMyPiAgentDir: directory, env: { HOME: directory, LETTA_LOCAL_BACKEND_DIR: directory } })
    vi.mocked(withNativeWorker).mockImplementation(async () => {
      writeFileSync(join(directory, 'attempted-worker.txt'), 'preserve these live Worker files')
      throw attempted
    })
    const fixture = fixtures.get('privateMcpLettaWorkspace')
    if (typeof fixture !== 'function')
      throw new Error('The private Letta MCP fixture definition is absent.')
    await expect(fixture({ page: {}, leapmuxServer: { mockModelUrl: 'http://127.0.0.1:1' } }, async () => {})).rejects.toBe(attempted)
    expect(withNativeWorker).toHaveBeenCalledTimes(1)
    expect(readFileSync(join(directory, 'attempted-worker.txt'), 'utf8')).toBe('preserve these live Worker files')
  })
})
