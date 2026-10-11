import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { AgentInfoSchema, AgentProvider, AgentStatus, ListAgentMessagesResponseSchema, MessagePageAnchor } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorNativeToolOutput } from './nativeExecutionScenario'

const calls = vi.hoisted(() => ({ current: vi.fn(), agent: vi.fn(), worker: vi.fn() }))
vi.mock('../helpers/nativeScenario', () => ({ currentNativeAgent: calls.current, nativeAgentById: calls.agent }))
vi.mock('../helpers/api', () => ({ getTestChannel: async () => ({ callWorker: calls.worker }) }))
const encoder = new TextEncoder()
// The reader uses only the channel and current agent. Browser and model handles remain opaque.
const context: ManagedNativeScenarioContext = {
  page: {} as Page,
  modelScript: {} as ModelScript,
  provider: AgentProvider.CURSOR,
  providerAgent: { provider: AgentProvider.CURSOR, prefix: 'native-e2e' },
  workspaceId: 'workspace',
  leapmuxServer: { hubUrl: 'http://unit.invalid', adminToken: 'unit-token', workerId: 'worker' },
}
/**
 * The refusal of a call with no single completed result in the current session. The reader counts those results with
 * Playwright's `toHaveLength`, and Playwright's `expect` fails with the name of its matcher.
 */
const NOT_ONE_RESULT = { matcherResult: { name: 'toHaveLength', pass: false } }
function result(id = 'current-result', session = 'current-session', rawOutput: unknown = { stdout: 'ACTUAL_OUTPUT' }) {
  return makeMessage({ id, seq: 1n, spanId: 'native-call', agentSessionId: session, content: rawContent({ toolCallId: 'native-call', status: 'completed', rawOutput }) })
}
beforeEach(() => {
  vi.resetAllMocks()
  const agent = create(AgentInfoSchema, { id: 'parent', agentSessionId: 'current-session', status: AgentStatus.ACTIVE })
  calls.current.mockResolvedValue(agent)
  calls.agent.mockResolvedValue(agent)
})

describe('cursorNativeToolOutput', () => {
  it('retains the exact current native call output', async () => {
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [result()] }))
    expect(await cursorNativeToolOutput(context, 'native-call')).toEqual({ stdout: 'ACTUAL_OUTPUT' })
  })

  it('refuses an empty native session before decoding a matching virtual-child result', async () => {
    const child = create(AgentInfoSchema, { id: 'child', status: AgentStatus.ACTIVE, parentAgentId: 'parent', spawnSpanId: 'native-spawn', rootAgentId: 'root' })
    calls.current.mockResolvedValue(child)
    calls.agent.mockResolvedValue(child)
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [result('child-result', '')] }))
    await expect(cursorNativeToolOutput(context, 'native-call')).rejects.toThrow('nonempty session ID')
    expect(calls.worker).toHaveBeenCalledTimes(1)
    const damaged = result('damaged-result', '')
    damaged.content = encoder.encode('{broken')
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [damaged] }))
    await expect(cursorNativeToolOutput(context, 'native-call')).rejects.toThrow('nonempty session ID')
  })

  it('refuses a stale-session result even when its native call ID matches', async () => {
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [result('stale-result', 'old-session', { stdout: 'STALE_OUTPUT' })] }))
    await expect(cursorNativeToolOutput(context, 'native-call')).rejects.toMatchObject(NOT_ONE_RESULT)
  })

  it('selects the current result without treating a stale same-call result as a duplicate', async () => {
    const current = result()
    current.seq = 2n
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [result('stale-result', 'old-session', { stdout: 'STALE_OUTPUT' }), current] }))
    expect(await cursorNativeToolOutput(context, 'native-call')).toEqual({ stdout: 'ACTUAL_OUTPUT' })
  })

  it('reads an earlier result page instead of assuming the latest page contains the call', async () => {
    const later = makeMessage({ id: 'later-message', seq: 500n, agentSessionId: 'current-session', content: encoder.encode('{}') })
    calls.worker.mockImplementation(async (_worker, _method, _requestSchema, _responseSchema, request) => {
      return request.anchor === MessagePageAnchor.OLDEST
        ? create(ListAgentMessagesResponseSchema, { messages: [result()], hasMore: true })
        : create(ListAgentMessagesResponseSchema, { messages: [later] })
    })
    expect(await cursorNativeToolOutput(context, 'native-call')).toEqual({ stdout: 'ACTUAL_OUTPUT' })
  })

  it('preserves the exactly-one completed current-result guard', async () => {
    const duplicate = result('duplicate')
    duplicate.seq = 2n
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [result(), duplicate] }))
    await expect(cursorNativeToolOutput(context, 'native-call')).rejects.toMatchObject(NOT_ONE_RESULT)
  })

  it.each([
    { label: 'wrong native call', body: { toolCallId: 'another-call', status: 'completed', rawOutput: {} } },
    { label: 'unfinished native call', body: { toolCallId: 'native-call', status: 'in_progress', rawOutput: {} } },
    { label: 'missing native output', body: { toolCallId: 'native-call', status: 'completed' } },
    { label: 'non-object native output', body: { toolCallId: 'native-call', status: 'completed', rawOutput: [] } },
  ])('refuses a $label', async ({ body }) => {
    const source = result()
    source.content = encoder.encode(JSON.stringify(body))
    calls.worker.mockResolvedValue(create(ListAgentMessagesResponseSchema, { messages: [source] }))
    await expect(cursorNativeToolOutput(context, 'native-call')).rejects.toMatchObject(NOT_ONE_RESULT)
  })
})
