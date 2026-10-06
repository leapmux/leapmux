import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { DeepseekHarnessGoalOwner } from './goalCleanup'
import { expect } from '@playwright/test'
import { AgentGoalAction, AgentStatus, CloseAgentRequestSchema, CloseAgentResponseSchema, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, UpdateAgentGoalRequestSchema, UpdateAgentGoalResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WorktreeAction } from '../../../src/generated/proto/leapmux/v1/common_pb'
import { LastTabCloseTarget } from '../../../src/generated/proto/leapmux/v1/git_pb'
import { TabType } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { getTestChannel } from '../helpers/api'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { inspectLastTabCloseViaAPI } from '../helpers/worktree'
import { cleanupDeepseekHarnessGoal } from './goalCleanup'

type GoalCleanupContext = Pick<ManagedNativeScenarioContext, 'leapmuxServer'> & {
  modelScript: Pick<ModelScript, 'releaseGateIfHeld'>
}

export function deepseekHarnessGoalOwner(agent: Pick<AgentInfo, 'id' | 'rootAgentId' | 'parentAgentId'>, workerId: string): DeepseekHarnessGoalOwner {
  if (!agent.id.trim() || agent.parentAgentId || (agent.rootAgentId && agent.rootAgentId !== agent.id) || !workerId.trim())
    throw new Error('The native goal scenario requires its owned root and Worker.')
  return Object.freeze({ agentId: agent.id, workerId })
}

/** Capture the owned root before a failed view or a selected child can change the UI state. */
export async function captureDeepseekHarnessGoalOwner(context: ManagedNativeScenarioContext): Promise<DeepseekHarnessGoalOwner> {
  const agent = await currentNativeAgent(context)
  return deepseekHarnessGoalOwner(agent, context.leapmuxServer.workerId)
}

/** Stop the captured goal through the Worker before any held model reply releases. */
export async function withDeepseekHarnessGoalCleanup<T>(context: GoalCleanupContext, owner: DeepseekHarnessGoalOwner, gates: readonly string[], operation: () => Promise<T>): Promise<T> {
  const server = context.leapmuxServer
  const capturedOwner = Object.freeze({ ...owner })
  if (capturedOwner.workerId !== server.workerId)
    throw new Error('The native goal cleanup owner does not belong to this scenario Worker.')
  return withCleanup(operation, () => cleanupDeepseekHarnessGoal(capturedOwner, {
    clearGoal: async (captured) => {
      const channel = await getTestChannel(server.hubUrl, server.adminToken)
      await channel.callWorker(captured.workerId, 'UpdateAgentGoal', UpdateAgentGoalRequestSchema, UpdateAgentGoalResponseSchema, { agentId: captured.agentId, action: AgentGoalAction.CLEAR })
      const snapshot = await channel.callWorker(captured.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId: captured.agentId, limit: 1 })
      if (!snapshot.goalLoaded || snapshot.goal)
        throw new Error('The Worker did not confirm removal of the captured native goal.')
    },
    closeAgent: async (captured) => {
      const channel = await getTestChannel(server.hubUrl, server.adminToken)
      const response = await channel.callWorker(captured.workerId, 'CloseAgent', CloseAgentRequestSchema, CloseAgentResponseSchema, { agentId: captured.agentId, worktreeAction: WorktreeAction.KEEP })
      if (!response.result || response.result.failureMessage || response.result.failureDetail)
        throw new Error('The Worker did not confirm closure of the captured native root.')
      await retryUntilPass(async () => {
        const agent = await nativeAgentById(context, captured.agentId)
        if (agent) {
          expect({ status: agent.status, closed: agent.closedAt !== '' }, 'the Worker closed the captured native root').toEqual({ status: AgentStatus.INACTIVE, closed: true })
          return
        }
        const inspection = await inspectLastTabCloseViaAPI(server.hubUrl, server.adminToken, captured.workerId, TabType.AGENT, captured.agentId)
        expect({ target: inspection.target, shouldPrompt: inspection.shouldPrompt }, 'the Worker holds no tab of the captured native root')
          .toEqual({ target: LastTabCloseTarget.NONE, shouldPrompt: false })
      })
    },
    releaseReplies: () => finishCleanup(gates.map(gate => context.modelScript.releaseGateIfHeld(gate))),
  }))
}
