import type { ToolRowOutcome } from '../toolOutcomeLabel'

/**
 * Every state a tool row's header can draw.
 *
 * A CLOSED set, for the reason {@link ToolKind} gives: the header tests the value
 * against three words, so a typo such as `'canceled'` compiled into a header that
 * silently never drew. The empty string is the state "the provider states no
 * status", which an Agent Client Protocol update that omits the field sends.
 */
export const TOOL_ROW_STATUSES = ['', 'pending', 'in_progress', 'completed', 'failed', 'cancelled'] as const

export type ToolRowStatus = (typeof TOOL_ROW_STATUSES)[number]

const KNOWN_TOOL_ROW_STATUSES: ReadonlySet<string> = new Set(TOOL_ROW_STATUSES)

/**
 * Narrow a wire value to one row status.
 *
 * A word LeapMux does not know becomes the empty string, because the header can
 * state nothing about it — which is what an unknown word already did, silently.
 */
export function toolRowStatus(value: string | undefined): ToolRowStatus {
  return value !== undefined && KNOWN_TOOL_ROW_STATUSES.has(value) ? value as ToolRowStatus : ''
}

/**
 * The status of a row whose provider reports no status word of its own.
 *
 * Pi and ZCode each derive it from the same three facts, in the same order, with
 * the same answers, so it lives here once. LeapMux's own completion wins over the
 * frame, for the reason `retainedOutcome` gives: a retained frame still reads as a
 * call in progress, and only the completion states that the turn cut it.
 *
 * Copilot does NOT use this. Its own derivation carries an `error.code` case and a
 * separate retained-start row, so it keeps its local four-member union, which this
 * type admits.
 */
export function toolStatusFor(outcome: ToolRowOutcome | null, isError: boolean, finished: boolean): ToolRowStatus {
  if (outcome === 'interrupted')
    return 'cancelled'
  if (isError || outcome === 'failed')
    return 'failed'
  return finished ? 'completed' : 'in_progress'
}
