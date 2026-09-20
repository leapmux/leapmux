import type { PromptFormat } from '../promptFormat'
import type { RunStatus } from '../runStatus'
import type { ToolMetadataEntry } from '../toolMetadata'

/** The launch of one subagent: what it was asked to do, and the prompt that asked it. */
export interface AgentRequest {
  description: string
  agentType?: string
  prompt: string
  promptLabel?: string
  promptFormat?: PromptFormat
  metadata?: ToolMetadataEntry[]
  /**
   * The background-task row this launch created, when it created exactly ONE.
   *
   * The registry knows what the subagent is actually DOING, which a launch that states
   * its tool alone does not: Codex's `spawnAgent` describes itself as "Subagent" and
   * nothing more. The renderer prefers the registry's title, because the row then
   * reads the same as the Background tasks list it points at. {@link AgentRun} carries
   * the same key for the same reason, one card down.
   */
  registryKey?: string
}

/**
 * How one subagent run ended: {@link RunStatus}, whole.
 *
 * `agentRunStatesOutcome` reads the glyph table keyed by this, and the row suppresses
 * its shared outcome header only for a run that states one of its own.
 */
export type AgentRunStatus = RunStatus

/**
 * ONE subagent the call ran, after its provider resolved the native state.
 *
 * One call can launch several, so the result holds a list of these and
 * `results/agentResult.tsx` draws one card for each.
 */
export interface AgentRun {
  description: string
  registryKey?: string
  agentId: string
  /**
   * The state in the words of the provider that reported it, when those words say
   * MORE than the outcome does: "launched asynchronously", "status unavailable",
   * "not found", "partial".
   *
   * Absent means the outcome IS the word, which {@link agentRunStatusLabel} answers.
   * Display PROSE, and deliberately open. It is NOT `ToolCallBase.status`, which is
   * the row's own closed status word, and it is not {@link AgentRun.outcome}, which is
   * the closed set the glyph and the shared header read. Under the bare name `status`
   * a reader could write `run.status === 'completed'`, which compiles and happens to
   * hold for some providers and not others.
   */
  statusLabel?: string
  outcome: AgentRunStatus
  metadata: ToolMetadataEntry[]
  body: string
  bodyLabel?: string
}

export interface AgentResult { agents: AgentRun[] }

/**
 * The word one run's card states, from the provider's own prose or from the outcome.
 *
 * `unknown` states NOTHING: the card then describes what the subagent is rather than
 * claiming a state no provider reported.
 */
export function agentRunStatusLabel(run: AgentRun): string {
  // EMPTY is absent, not a label. Every producer derives this from a picked wire
  // string, and `pickString` answers `''` for a key the record does not carry -- so
  // `??` alone let an agent record with no status word suppress the outcome word this
  // function exists to supply, and the card headed itself with no state at all.
  return run.statusLabel || (run.outcome === 'unknown' ? '' : run.outcome)
}
