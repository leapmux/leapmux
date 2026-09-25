import type { ParsedMessageContent } from '~/lib/messageParser'
import { OH_MY_PI_EVENT, OH_MY_PI_FRAME_FIELD, OH_MY_PI_SUPPLEMENT } from '~/generated/contracts/ohmypi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'

// Two vocabularies meet in this module, and each object keeps its own.
// `OH_MY_PI_FRAME_FIELD` holds the fields of omp's own frame, and
// `OH_MY_PI_SUPPLEMENT` the fields of the envelope LeapMux writes beside it. They
// spell the same words today; a rename on one side must not move the read of the
// other.

/**
 * Put a retained call's partial result on its start frame, for display.
 *
 * omp puts a call's result on its `tool_execution_end` and sends none when the turn
 * ends first, so the last `tool_execution_update` is the only copy. The worker stores
 * omp's own START frame as the closing row and keeps that copy beside it.
 *
 * The identity keys are checked first: a supplement that states another call, or
 * another tool, cannot reach this row. The worker's `ResolveProviderData` makes the
 * same join.
 */
export function resolveOhMyPiMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = parsed.parentObject
  const extra = parsed.supplementalContent
  if (!original || original.type !== OH_MY_PI_EVENT.ToolExecutionStart || !isObject(extra))
    return original
  const partial = extra[OH_MY_PI_SUPPLEMENT.PartialResult]
  if (!isObject(partial)
    || !pickString(original, OH_MY_PI_FRAME_FIELD.ToolCallID)
    || !pickString(original, OH_MY_PI_FRAME_FIELD.ToolName)
    || extra[OH_MY_PI_SUPPLEMENT.ToolCallID] !== original[OH_MY_PI_FRAME_FIELD.ToolCallID]
    || extra[OH_MY_PI_SUPPLEMENT.ToolName] !== original[OH_MY_PI_FRAME_FIELD.ToolName]) {
    return original
  }
  // Resolving an already-resolved frame returns the SAME object, so a caller that
  // resolves twice does not rebuild the row.
  if (original[OH_MY_PI_FRAME_FIELD.Result] === partial)
    return original
  return { ...original, [OH_MY_PI_FRAME_FIELD.Result]: partial }
}
