import type { GoalAction, GoalProgress, SessionGoal } from './chatGoal'
import type { AgentGoalAction, AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import { goalActionsFromProto, protoGoalToStore } from './chatGoal'
import { createPerAgentStore, createPerAgentValueStore } from './chatPerAgentStore'

export type GoalReplacementResult = 'stale' | 'applied' | 'progress-cleared'

// Keep the goal and its progress in separate per-agent values.
// AgentGoalChanged supplies these fields after a transition:
// - The objective.
// - The status.
// - The supported actions.
// Ephemeral session-info messages carry only changed progress counters and persist no row.
// A provider can advance those counters after each completed tool call.
// Separate values prevent each counter update from replacing the goal card.

export function createGoalStore() {
  const goal = createPerAgentValueStore<SessionGoal>()
  const progress = createPerAgentStore<GoalProgress>({})
  // Retain supported actions without a goal so the empty state can enable Set a goal.
  const actions = createPerAgentStore<GoalAction[]>([])
  // Keep the applied timestamp outside reactive state because only the next write reads it.
  const appliedAt = new Map<string, string>()
  return {
    get: goal.get,
    progress: progress.get,
    supportedActions: actions.get,
    clear(agentId: string) {
      goal.clear(agentId)
      progress.clear(agentId)
      actions.clear(agentId)
      appliedAt.delete(agentId)
    },
    remove(agentId: string) {
      goal.remove(agentId)
      progress.remove(agentId)
      actions.remove(agentId)
      appliedAt.delete(agentId)
    },
    /**
     * Replace the goal with the worker's authoritative value. Undefined means no goal.
     * Reconcile unchanged fields so the status animation and an open tooltip retain their identity.
     * See PerAgentValueStore.setReconciled.
     *
     * updatedAt orders the three producers:
     * - Live AgentGoalChanged events.
     * - WatchEvents replay after subscription.
     * - ListAgentMessages during the initial history load.
     * A strictly older history response must not restore a goal after a newer clear.
     * Keep this guard on the shared write path so every caller uses it.
     *
     * Equal timestamps still apply because agent registration republishes the supported actions beside an unchanged goal.
     * Rejecting that publication would leave the goal controls disabled.
     * Worker timestamps use fixed-width UTC text, such as 2025-06-15T10:30:45.123Z.
     * String comparison therefore orders them without date parsing.
     */
    replace(agentId: string, next: ProtoAgentGoal | undefined, supportedActions: AgentGoalAction[], updatedAt: string): GoalReplacementResult {
      const applied = appliedAt.get(agentId)
      if (applied !== undefined && updatedAt < applied)
        return 'stale'
      appliedAt.set(agentId, updatedAt)
      // Apply supported actions after the same timestamp guard as the goal.
      // Republication uses the current row timestamp and an unchanged goal, so an equal timestamp safely applies.
      // Applying actions before the guard could restore Pause and Clear from history after the agent process exits.
      actions.set(agentId, goalActionsFromProto(supportedActions))
      const previous = goal.get(agentId)
      const incoming = next ? protoGoalToStore(next) : undefined
      // Clear progress when its goal disappears or receives a different identity or objective.
      // Otherwise, a fresh goal would show the previous goal's counters until its next progress update.
      // Compare native IDs when both goals supply them.
      // Otherwise, compare createdAt because a provider without a native goal ID identifies a restart through that timestamp.
      const differentIdentity = incoming?.nativeId && previous?.nativeId
        ? incoming.nativeId !== previous.nativeId
        : incoming?.createdAt !== previous?.createdAt
      const progressCleared = !incoming || differentIdentity || incoming.objective !== previous?.objective
      if (progressCleared)
        progress.clear(agentId)
      goal.setReconciled(agentId, incoming)
      return progressCleared ? 'progress-cleared' : 'applied'
    },
    /**
     * Merge the supplied progress counters.
     * A provider can omit counters, and the session-info channel suppresses unchanged values per key.
     * A tokens_used update must therefore retain an earlier iteration count.
     */
    setProgress(agentId: string, next: GoalProgress) {
      progress.set(agentId, { ...progress.get(agentId), ...next })
    },
  }
}

export type GoalStore = ReturnType<typeof createGoalStore>
