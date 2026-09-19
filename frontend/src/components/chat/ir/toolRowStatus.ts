import type { ToolOutcomeWord } from './toolOutcomeLabel'

/**
 * Every state a tool row's header can draw.
 *
 * A CLOSED set, for the reason {@link ToolKind} gives: the header tests the value
 * against three words, so a typo such as `'canceled'` compiled into a header that
 * silently never drew. The empty string is the state "the provider states no
 * status", which an Agent Client Protocol update that omits the field sends.
 *
 * `declined` is the call a reader REFUSED, so the tool never ran. It is not a
 * failure: no command produced an exit code and no patch touched a file. Codex
 * reports it on a `commandExecution` or a `fileChange` whose approval the reader
 * denied, and Cursor reports it on a rejected web approval.
 */
export const TOOL_ROW_STATUSES = ['', 'pending', 'in_progress', 'completed', 'failed', 'cancelled', 'declined'] as const

export type ToolRowStatus = (typeof TOOL_ROW_STATUSES)[number]

/** The statuses a call that has not answered yet can hold. Invariant I1's left half. */
export type UnfinishedToolStatus = '' | 'pending' | 'in_progress'

/** The statuses that end a call. Every one of them admits a result; each states how it ended. */
export type FinishedToolStatus = 'completed' | 'failed' | 'cancelled' | 'declined'

/** The two halves as values, so a test can walk them without restating the union. */
export const UNFINISHED_TOOL_STATUSES = ['', 'pending', 'in_progress'] as const
export const FINISHED_TOOL_STATUSES = ['completed', 'failed', 'cancelled', 'declined'] as const

/**
 * Whether one status ends its call.
 *
 * The finished/unfinished split is the rule the whole lifecycle turns on -- the
 * result a call may carry, the pictures it may hold, the header it draws -- and it
 * was restated as a four-way comparison at every site that needed it. One
 * predicate states it once, over the two halves of the union above.
 */
export function isFinishedToolStatus(status: ToolRowStatus): status is FinishedToolStatus {
  return status === 'completed' || status === 'failed' || status === 'cancelled' || status === 'declined'
}

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

/** The status an outcome word states, for the three that end a call. */
export function statusForOutcome(outcome: ToolOutcomeWord): FinishedToolStatus {
  switch (outcome) {
    case 'failed':
      return 'failed'
    case 'interrupted':
      return 'cancelled'
    default:
      return 'declined'
  }
}

/**
 * The outcome word a row's own status states, or null when the status states none.
 *
 * Three statuses end a call in a way the reader must see, and each maps to one word
 * of the shared outcome vocabulary. Every other status -- including `completed` --
 * needs no such header, because the body states the result.
 *
 * One table, because the header, the MCP card's failure label and the toolbar each
 * asked the question separately, and a status added to the union reached only the
 * site somebody remembered.
 */
export function toolRowStatusOutcome(status: ToolRowStatus): ToolOutcomeWord | null {
  switch (status) {
    case 'failed':
      return 'failed'
    case 'cancelled':
      return 'interrupted'
    case 'declined':
      return 'declined'
    default:
      return null
  }
}
