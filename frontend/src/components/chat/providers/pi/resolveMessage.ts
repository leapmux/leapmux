import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_EVENT, PI_RESULT_FIELD, PI_SUPPLEMENT } from '~/generated/contracts/pi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'

// Native frame keys and LeapMux partial-result keys have separate owners.

/**
 * Put a retained call's partial result on its start frame.
 *
 * Pi sends no completion result when the turn ends first.
 * The worker stores the native start frame beside the last tool_execution_update result.
 * Both identity keys must match before that saved result reaches the row.
 */
function resolvePiIncompleteTool(
  original: Record<string, unknown>,
  extra: Record<string, unknown>,
): Record<string, unknown> {
  const partial = extra[PI_SUPPLEMENT.PartialResult]
  if (!isObject(partial) || !pickString(original, PI_RESULT_FIELD.ToolCallID) || !pickString(original, PI_RESULT_FIELD.ToolName)
    || extra[PI_SUPPLEMENT.ToolCallID] !== original[PI_RESULT_FIELD.ToolCallID]
    || extra[PI_SUPPLEMENT.ToolName] !== original[PI_RESULT_FIELD.ToolName]) {
    return original
  }
  // Return the same object on repeated resolution to prevent a second row rebuild.
  if (original[PI_RESULT_FIELD.Result] === partial)
    return original
  return { ...original, [PI_RESULT_FIELD.Result]: partial }
}

/** Keep retained partial results on their exact native start frames. */
export function resolvePiMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = parsed.parentObject
  const extra = parsed.supplementalContent
  return original?.type === PI_EVENT.ToolExecutionStart && isObject(extra)
    ? resolvePiIncompleteTool(original, extra)
    : original
}
