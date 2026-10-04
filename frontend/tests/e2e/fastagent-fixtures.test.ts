import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { openFastAgentAgent } from './fastagent-fixtures'
import { openAgentViaAPI } from './helpers/api'
import { FAST_AGENT_MOCK_MODEL, MOCK_MODELS } from './helpers/mockAgentEnvironment'
import { createTestDirectory } from './helpers/runDirectory'

vi.mock('./helpers/api', () => ({ openAgentViaAPI: vi.fn() }))
vi.mock('./helpers/runDirectory', () => ({ createTestDirectory: vi.fn(() => '/fast-agent-test') }))
vi.mock('./fixtures', () => ({ test: { extend: () => ({}) }, expect: () => {} }))
vi.mock('./acp-fixture-factory', async () => {
  const { AgentProvider } = await import('../../src/generated/proto/leapmux/v1/agent_pb')
  return {
    AgentProvider,
    authenticateACPWorkspace: vi.fn(),
    createACPWorkspace: vi.fn(),
    detectACPSkipReason: vi.fn(() => null),
  }
})

const server = { hubUrl: 'http://hub.test', adminToken: 'session', workerId: 'worker' }

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(openAgentViaAPI).mockResolvedValue('agent')
})

describe('openFastAgentAgent', () => {
  it('passes a model override as the selected top-level model', async () => {
    await openFastAgentAgent(server, 'workspace', { model: MOCK_MODELS.zai })

    expect(createTestDirectory).toHaveBeenCalledExactlyOnceWith('fastagent-e2e-wd-')
    expect(openAgentViaAPI).toHaveBeenCalledWith(
      server.hubUrl,
      server.adminToken,
      server.workerId,
      'workspace',
      '/fast-agent-test',
      expect.objectContaining({
        model: MOCK_MODELS.zai,
        optionValues: expect.objectContaining({ model: MOCK_MODELS.zai }),
      }),
    )
  })

  it('keeps the pinned model when the caller selects another option', async () => {
    await openFastAgentAgent(server, 'workspace', { mode: 'plan' })

    expect(openAgentViaAPI).toHaveBeenCalledWith(
      server.hubUrl,
      server.adminToken,
      server.workerId,
      'workspace',
      '/fast-agent-test',
      {
        agentProvider: AgentProvider.FAST_AGENT,
        model: FAST_AGENT_MOCK_MODEL,
        optionValues: { mode: 'plan' },
      },
    )
  })
})
