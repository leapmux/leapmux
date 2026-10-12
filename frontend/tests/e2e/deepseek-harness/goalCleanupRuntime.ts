import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { DeepseekHarnessGoalOwner } from './goalCleanup'
import { AgentGoalAction, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, UpdateAgentGoalRequestSchema, UpdateAgentGoalResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from '../helpers/api'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { closeNativeAgentAndWait } from '../helpers/workerTabs'
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
    // The owner check above makes `captured.workerId` the scenario Worker, which the shared close reaches.
    closeAgent: captured => closeNativeAgentAndWait(context, captured.agentId),
    releaseReplies: () => finishCleanup(gates.map(gate => context.modelScript.releaseGateIfHeld(gate))),
  }))
}
