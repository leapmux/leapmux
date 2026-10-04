import type { Locator } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { Code } from '@connectrpc/connect'
import { expect } from '@playwright/test'
import { AgentInputKind, EnqueueAgentInputRequestSchema, EnqueueAgentInputResponseSchema, InterruptAgentRequestSchema, InterruptAgentResponseSchema, ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from './api'
import { withCleanup } from './cleanup'
import { listAgents, openChildTabFromRow } from './subagentRegistry'

export interface RunningNativeChild {
  row: Locator
  childId: string
  parentId: string
  finish: () => Promise<void>
}

/** Check a native child route while its original task still runs. */
export async function expectUnsupportedSubagent(
  context: ManagedNativeScenarioContext,
  options: { operation: 'send' | 'interrupt', openChild: () => Promise<RunningNativeChild> },
): Promise<void> {
  const child = await options.openChild()
  await withCleanup(async () => {
    expect(child.childId).not.toBe('')
    expect(child.parentId).not.toBe('')
    await expect(child.row).toHaveAttribute('data-status', 'running')
    const { hubUrl, adminToken, workerId } = context.leapmuxServer
    const agents = await listAgents(hubUrl, adminToken, workerId, [child.childId, child.parentId])
    const info = agents?.find(agent => agent.id === child.childId)
    const parent = agents?.find(agent => agent.id === child.parentId)
    expect(info).toBeDefined()
    expect(parent).toBeDefined()
    if (!info || !parent)
      throw new Error('The native child or parent has no Worker record.')
    expect(info.parentAgentId).toBe(child.parentId)
    expect(info.rootAgentId).toBe(parent.rootAgentId)
    await openChildTabFromRow(context.page, child.row)
    const channel = await getTestChannel(hubUrl, adminToken)
    if (options.operation === 'interrupt') {
      expect(info.acceptsInterrupt).toBe(false)
      expect(parent.acceptsInterrupt).toBe(true)
      await expect(context.page.getByTestId('interrupt-button').filter({ visible: true })).toHaveCount(0)
      await expect(channel.callWorker(workerId, 'InterruptAgent', InterruptAgentRequestSchema, InterruptAgentResponseSchema, {
        agentId: child.childId,
      })).rejects.toMatchObject({ source: 'rpc', code: Code.FailedPrecondition, message: 'this subagent cannot be interrupted' })
    }
    else {
      expect(info.acceptsMessages).toBe(false)
      expect(parent.acceptsMessages).toBe(true)
      await expect(context.page.getByTestId('composer-editor').locator('.ProseMirror').filter({ visible: true })).toHaveAttribute('contenteditable', 'false')
      const inputId = crypto.randomUUID()
      const before = await channel.callWorker(workerId, 'ListAgentInputQueue', ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema, { agentId: child.childId })
      expect(before.snapshot).toBeDefined()
      await expect(channel.callWorker(workerId, 'EnqueueAgentInput', EnqueueAgentInputRequestSchema, EnqueueAgentInputResponseSchema, {
        agentId: child.childId,
        inputId,
        text: 'CHILD_MESSAGE_MUST_NOT_REACH_NATIVE_MODEL',
        kind: AgentInputKind.USER_MESSAGE,
      })).rejects.toMatchObject({ source: 'rpc', code: Code.InvalidArgument, message: 'invalid queued agent input: this agent does not accept that input' })
      const after = await channel.callWorker(workerId, 'ListAgentInputQueue', ListAgentInputQueueRequestSchema, ListAgentInputQueueResponseSchema, { agentId: child.childId })
      expect(after.snapshot).toEqual(before.snapshot)
      const state = await context.modelScript.status()
      expect(state.requests.some(request => JSON.stringify(request.body).includes('CHILD_MESSAGE_MUST_NOT_REACH_NATIVE_MODEL'))).toBe(false)
    }
    await expect(child.row).toHaveAttribute('data-status', 'running')
  }, () => child.finish())
}
