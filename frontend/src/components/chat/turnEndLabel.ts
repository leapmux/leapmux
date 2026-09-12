import { formatDuration } from './rendererUtils'

/**
 * How a turn ended, in LeapMux's own words.
 *
 * Every provider spells its own outcome -- Codex says `completed`, the Agent Client
 * Protocol says `end_turn`, Pi says `aborted`, ZCode says `cancelled` -- and each one
 * used to reach the reader unchanged. One transcript then said "Took 2.0s", the next
 * said "Turn completed", and a third said "Turn ended (cancelled)" for the same three
 * things. A reader who works across providers had to learn three vocabularies.
 */
export type TurnOutcome = 'ended' | 'interrupted' | 'failed'

const OUTCOME_WORDS: Record<TurnOutcome, string> = {
  ended: 'Turn ended',
  interrupted: 'Turn interrupted',
  failed: 'Turn failed',
}

export interface TurnEndLabelParts {
  /** Milliseconds the turn took, when the provider reports them. */
  durationMs?: number | null
  /**
   * Extra words for the parentheses, after the duration: a stop reason this
   * vocabulary has no outcome for (`max_tokens`), a failure code, a pending retry.
   * An empty or absent entry is dropped.
   */
  qualifiers?: Array<string | false | null | undefined>
  /** The provider's own explanation, which follows an em dash. */
  reason?: string
}

/**
 * The label one turn-end divider shows.
 *
 * The shape is `Turn ended (2.0s, max_tokens) — reason`, and every part after the
 * outcome is optional. The outcome words are LeapMux's; everything in the parentheses
 * and after the dash is the provider's own report.
 */
export function turnEndLabel(outcome: TurnOutcome, parts: TurnEndLabelParts = {}): string {
  const inside = [
    parts.durationMs !== null && parts.durationMs !== undefined ? formatDuration(parts.durationMs) : '',
    ...(parts.qualifiers ?? []),
  ].filter((part): part is string => typeof part === 'string' && part !== '')
  const head = inside.length > 0 ? `${OUTCOME_WORDS[outcome]} (${inside.join(', ')})` : OUTCOME_WORDS[outcome]
  const reason = parts.reason?.trim()
  return reason ? `${head} — ${reason}` : head
}
