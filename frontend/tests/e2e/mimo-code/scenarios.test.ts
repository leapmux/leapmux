import type { NativeContextFixtures } from '../helpers/nativeScenario'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeContext } from './scenarios'

const resolver = vi.hoisted(() => vi.fn(async () => 'actual-native-part'))
vi.mock('./toolRowId', () => ({ mimoToolRowIdResolver: () => resolver }))

describe('nativeContext', () => {
  it('retains the MiMo provider resolver without resolving a row during construction', async () => {
    const fixtures = { page: {}, modelScript: {}, workspaceId: 'workspace', leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'worker' } } as NativeContextFixtures
    const context = await nativeContext(fixtures)
    expect(context.provider).toBe(AgentProvider.MIMO_CODE)
    expect(context.resolveToolRowId).toBe(resolver)
    expect(resolver).not.toHaveBeenCalled()
    await context.resolveToolRowId?.({ callId: 'model-call', agentId: 'selected-child' })
    expect(resolver).toHaveBeenCalledExactlyOnceWith({ callId: 'model-call', agentId: 'selected-child' })
  })
})
