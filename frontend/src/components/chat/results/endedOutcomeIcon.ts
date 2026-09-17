import type { LucideIcon } from 'lucide-solid'
import type { RunOutcome } from '../ir/runOutcome'
import Check from 'lucide-solid/icons/check'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import OctagonX from 'lucide-solid/icons/octagon-x'

/**
 * The glyph each ENDED outcome takes, for both cards that state one.
 *
 * The subagent card and the task card each held these three mappings, under two
 * different words for the same outcome, so a change to the "it stopped" glyph reached
 * one card and not the other.
 *
 * Partial on purpose, and that absence is load-bearing for the subagent card:
 * `agentRunStatesOutcome` reads a missing entry as "this run has not ended", which is
 * how the row decides whether to draw its own shared outcome header. The task card
 * adds its own `running` glyph on top, because a task surface always states one of
 * four and its table must stay exhaustive.
 */
export const ENDED_OUTCOME_ICON = {
  completed: Check,
  failed: CircleAlert,
  stopped: OctagonX,
} as const satisfies Partial<Record<RunOutcome, LucideIcon>>
