import { MESSAGE_METADATA_FIELD, TOOL_OUTCOME } from '~/generated/contracts/worker-vocab'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * What the worker concluded about a tool call whose own result the agent never sent.
 *
 * The agent supplies no such record, so this is LeapMux's CALCULATION and it travels
 * in the message-metadata column rather than inside the provider's bytes. The
 * sentence a reader sees lives in {@link toolOutcomeNote}, never in the database.
 */
export interface ToolOutcome {
  source: string
  outcome: string
}

export function parseToolOutcome(metadata: unknown): ToolOutcome | null {
  const note = pickObject(isObject(metadata) ? metadata : undefined, MESSAGE_METADATA_FIELD.ToolOutcome)
  if (!note)
    return null
  const source = pickString(note, TOOL_OUTCOME.FieldSource)
  const outcome = pickString(note, TOOL_OUTCOME.FieldOutcome)
  if (!source || !outcome)
    return null
  return { source, outcome }
}

/** The reader-facing sentence for one outcome note, or null when none applies. */
export function toolOutcomeNote(metadata: unknown): string | null {
  const outcome = parseToolOutcome(metadata)
  if (!outcome || outcome.source !== TOOL_OUTCOME.SourceBatchSummary)
    return null
  if (outcome.outcome === TOOL_OUTCOME.OutcomeSucceeded) {
    return 'The agent sent no result for this call. Its batch summary reports that the '
      + 'batch finished with no error.'
  }
  return 'The agent sent no result for this call. Its batch summary reports an error '
    + 'somewhere in the batch, so the outcome of this call is unknown.'
}
