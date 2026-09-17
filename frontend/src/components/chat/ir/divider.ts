import type { PromptFormat } from './promptFormat'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { SESSION_INFO_KEY } from '~/generated/contracts/session-info'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { pickNumber } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'

/**
 * The turn-end rule one row draws, after its provider read its own frame.
 *
 * Every provider ends a turn with a different shape -- a Claude `result` subtype, a
 * Codex `turn.status` plus its `turn.error`, a Pi `agent_end`, a ZCode `resultType`,
 * an Agent Client Protocol `stopReason`, a Copilot `session.idle`. Each plugin maps
 * its own into this, and ONE shared component draws it.
 */
export interface DividerIR {
  /** The label, e.g. "Turn ended", "Took 2.1s", "API Error: 529 …". */
  label: string
  /** Draw in the danger colour: the turn failed or was aborted. */
  isError?: boolean
  /**
   * A multi-line detail block below the label. Omit it (undefined), never pass the
   * empty string. A provider that shows its detail INLINE -- Codex writes
   * `message — details` -- folds it into `label` and leaves this unset.
   */
  detail?: string
  /** What the worker measured for the turn, when it measured anything. */
  meta?: DividerMetaIR
}

/** The turn totals the worker records beside a turn-end row. */
export interface DividerMetaIR {
  durationMs?: number
  costUsd?: number
  numToolUses?: number
}

/**
 * The turn totals the WORKER measured, which every provider's row carries the same way.
 *
 * The worker injects these onto the inner message for every provider, so one reader
 * serves all of them and no plugin states them again. They were parsed and dropped
 * before this: `extractResultMetadata` reads the cost for the session store and the
 * tool count for nothing at all, and neither reached the row the reader looks at.
 *
 * `durationMs` is carried but NOT drawn. Every provider already states the duration in
 * its own label -- Claude writes "Turn ended (12s)" -- so a second copy beside it would
 * say the same thing twice in a different voice.
 */
export function dividerMetaFromMessage(parsed: ParsedMessageContent): DividerMetaIR | undefined {
  const inner = getInnerMessage(parsed)
  if (!inner)
    return undefined
  // A subagent's totals are already inside the parent's, so its row states none.
  if (inner.parent_tool_use_id)
    return undefined
  // Each total rides only when the worker measured it, so an unmeasured field stays
  // absent rather than arriving as an explicitly undefined key.
  const durationMs = pickNumber(inner, MESSAGE_METADATA_FIELD.DurationMs, undefined)
  const costUsd = pickNumber(inner, SESSION_INFO_KEY.TotalCostUsd, undefined)
  const numToolUses = pickNumber(inner, MESSAGE_METADATA_FIELD.ToolUses, undefined)
  if (durationMs === undefined && costUsd === undefined && numToolUses === undefined)
    return undefined
  return {
    ...(durationMs !== undefined ? { durationMs } : {}),
    ...(costUsd !== undefined ? { costUsd } : {}),
    ...(numToolUses !== undefined ? { numToolUses } : {}),
  }
}

/**
 * A subagent prompt row: the instruction a parent sent to a child.
 *
 * Three surfaces produce one -- a provider's own subagent prompt row, Claude's
 * `agent_prompt`, and a prompt delivered into a child transcript -- and each used to
 * draw its own card. `promptFormat` says whether the body is Markdown or preformatted
 * text, which is the only difference between them that a reader can see.
 */
export interface AgentPromptIR {
  description?: string
  agentType?: string
  prompt: string
  promptFormat?: PromptFormat
}
