import type { GoalAction, GoalProgress, SessionGoal } from './chatGoal'
import type { AgentGoalAction, AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import { goalActionsFromProto, protoGoalToStore } from './chatGoal'
import { createPerAgentStore, createPerAgentValueStore } from './chatPerAgentStore'

// ---------------------------------------------------------------------------
// Session-goal slice
//
// Two per-agent values, because the goal arrives on two channels for one
// reason: the worker splits it by how fast each half changes.
//
//   - The GOAL itself (objective, status, capabilities) rides AgentGoalChanged,
//     which is broadcast only on a real transition.
//   - The PROGRESS counters ride the ephemeral session-info channel, which
//     dedups by value and never persists, because Codex advances them after
//     every completed tool call.
//
// Keeping them apart here is what keeps a 200-tool turn from rebuilding the
// goal card 200 times.
// ---------------------------------------------------------------------------

export function createGoalStore() {
  const goal = createPerAgentValueStore<SessionGoal>()
  const progress = createPerAgentStore<GoalProgress>({})
  // Apart from the goal, because it must exist when a goal does not: the empty
  // state's "Set a goal" button asks exactly this question.
  const actions = createPerAgentStore<GoalAction[]>([])
  // The stamp of the answer currently applied, per agent. NOT reactive: nothing
  // renders it, and it is read only to decide whether the next write lands.
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
     * Replace the agent's goal with the server-authoritative value. `undefined`
     * means the agent has none.
     *
     * The write RECONCILES, so a change to one field keeps the identity of
     * every other: the card's status dot does not restart its animation and a
     * tooltip under the pointer survives. See PerAgentValueStore.setReconciled.
     *
     * `updatedAt` is when the WORKER wrote the goal columns, and it is what
     * orders three writers that all answer this same question: the live
     * AgentGoalChanged broadcast, the WatchEvents replay on (re)subscribe, and
     * the ListAgentMessages cold load. The cold load is the stale one -- the
     * worker reads the row, and the browser applies the answer a round trip
     * later, so a clear that happened in between is undone by the older answer
     * landing second. Dropping a strictly older stamp here is what stops that.
     *
     * The guard lives on the ONE write path rather than at the two call sites,
     * so neither can forget it.
     *
     * An EQUAL stamp still applies, and that is not laziness: the worker
     * re-broadcasts an unchanged goal on purpose when an agent process
     * registers, to deliver the capabilities beside it. Dropping equal stamps
     * would discard exactly that broadcast and leave every control disabled.
     *
     * The layout is fixed-width UTC (`2025-06-15T10:30:45.123Z`), so a plain
     * string compare orders two stamps and no date is ever parsed.
     */
    replace(agentId: string, next: ProtoAgentGoal | undefined, supportedActions: AgentGoalAction[], updatedAt: string) {
      const applied = appliedAt.get(agentId)
      if (applied !== undefined && updatedAt < applied)
        return
      appliedAt.set(agentId, updatedAt)
      const previous = goal.get(agentId)
      const incoming = next ? protoGoalToStore(next) : undefined
      // The counters belong to ONE goal, so they are dropped whenever the goal
      // they measured is gone or replaced. Without this a fresh goal shows the
      // previous one's token count until its first progress broadcast lands,
      // which for Codex is one completed tool call away -- long enough to read.
      //
      // `createdAt` is what tells a replacement from an update: Codex puts no
      // goal id on the wire, so a restarted goal differs only by that stamp.
      if (!incoming || incoming.createdAt !== previous?.createdAt)
        progress.clear(agentId)
      goal.setReconciled(agentId, incoming)
      actions.set(agentId, goalActionsFromProto(supportedActions))
    },
    /**
     * Merge in the volatile counters.
     *
     * A MERGE rather than a replace, because a provider sends only the counters
     * it has and the session-info channel dedups per key: a broadcast carrying
     * just `tokens_used` must not blank an iteration count that arrived
     * earlier.
     */
    setProgress(agentId: string, next: GoalProgress) {
      progress.set(agentId, { ...progress.get(agentId), ...next })
    },
  }
}

export type GoalStore = ReturnType<typeof createGoalStore>
