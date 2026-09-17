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

/**
 * An outcome a PROVIDER states that LeapMux's own retained outcome has no word for.
 *
 * `declined` is the reader's refusal: the approval was denied, so the tool never
 * ran. `retainedOutcome` never answers it, because LeapMux concludes nothing
 * about a call whose provider reported the refusal itself. It stays out of
 * {@link ToolRowOutcome} for that reason, and the label table below carries both.
 */
export type ToolProviderOutcome = 'declined'

/** Every outcome word one tool row can state, whoever concluded it. */
export type ToolOutcomeWord = ToolRowOutcome | ToolProviderOutcome

const OUTCOME_WORDS: Record<ToolOutcomeWord, string> = {
  succeeded: 'Success',
  failed: 'Error',
  interrupted: 'Interrupted',
  declined: 'Declined',
}

/**
 * The word one tool row shows, with an optional qualifier in parentheses.
 *
 * The qualifier is the provider's own detail -- `exit 5` for a command, nothing for a
 * tool that reports no code. An empty or absent qualifier is dropped, so a row never
 * shows empty parentheses.
 */
export function toolOutcomeLabel(outcome: ToolOutcomeWord, qualifier?: string | null): string {
  const detail = qualifier?.trim()
  return detail ? `${OUTCOME_WORDS[outcome]} (${detail})` : OUTCOME_WORDS[outcome]
}
