/**
 * How one TOOL ROW ended, in LeapMux's own words.
 *
 * The same outcome reaches a reader through two routes. A provider states it in its
 * own frame -- an `isError` flag, a non-zero exit code, a `failed` tool status -- and
 * LeapMux states it in the completion column when it knows something the frame does
 * not. Each route used to spell the words itself, so one transcript said "Error" for a
 * failed fetch and the next said "Failed" for the same thing.
 *
 * This is `turnEndLabel` one level down: the turn-end divider already shares its three
 * words across every provider, and a tool row now shares these.
 */
export type ToolRowOutcome = 'succeeded' | 'failed' | 'interrupted'

const OUTCOME_WORDS: Record<ToolRowOutcome, string> = {
  succeeded: 'Success',
  failed: 'Error',
  interrupted: 'Interrupted',
}

/**
 * The word one tool row shows, with an optional qualifier in parentheses.
 *
 * The qualifier is the provider's own detail -- `exit 5` for a command, nothing for a
 * tool that reports no code. An empty or absent qualifier is dropped, so a row never
 * shows empty parentheses.
 */
export function toolOutcomeLabel(outcome: ToolRowOutcome, qualifier?: string | null): string {
  const detail = qualifier?.trim()
  return detail ? `${OUTCOME_WORDS[outcome]} (${detail})` : OUTCOME_WORDS[outcome]
}
