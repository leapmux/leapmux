import type { ToolOutcome } from '../model/toolOutcome'

const OUTCOME_WORDS: Record<ToolOutcome, string> = {
  succeeded: 'Success',
  failed: 'Error',
  interrupted: 'Interrupted',
  declined: 'Declined',
  incomplete: 'Incomplete',
}

/**
 * The word one tool row shows, with an optional qualifier in parentheses.
 *
 * The qualifier is the provider's own detail -- `exit 5` for a command, nothing for a
 * tool that reports no code. An empty or absent qualifier is dropped, so a row never
 * shows empty parentheses.
 */
export function toolOutcomeLabel(outcome: ToolOutcome, qualifier?: string | null): string {
  const detail = qualifier?.trim()
  return detail ? `${OUTCOME_WORDS[outcome]} (${detail})` : OUTCOME_WORDS[outcome]
}
