import type { ParsedMessageContent } from '~/lib/messageParser'
import { OH_MY_PI_EVENT, OH_MY_PI_FRAME_FIELD, OH_MY_PI_SUPPLEMENT } from '~/generated/contracts/ohmypi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'

// Native frame fields and LeapMux supplement fields have separate contracts.
// A change to one vocabulary must not change the other vocabulary.

/**
 * Keep a partial result beside its native start frame.
 * Both native identity fields must match before the partial result reaches the row.
 */
export function resolveOhMyPiMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = parsed.parentObject
  const extra = parsed.supplementalContent
  if (!original || !isObject(extra))
    return original
  if (original.type !== OH_MY_PI_EVENT.ToolExecutionStart)
    return original
  const partial = extra[OH_MY_PI_SUPPLEMENT.PartialResult]
  if (!isObject(partial)
    || !pickString(original, OH_MY_PI_FRAME_FIELD.ToolCallID)
    || !pickString(original, OH_MY_PI_FRAME_FIELD.ToolName)
    || extra[OH_MY_PI_SUPPLEMENT.ToolCallID] !== original[OH_MY_PI_FRAME_FIELD.ToolCallID]
    || extra[OH_MY_PI_SUPPLEMENT.ToolName] !== original[OH_MY_PI_FRAME_FIELD.ToolName]) {
    return original
  }
  // Return the same object when its partial result already matches the supplement.
  if (original[OH_MY_PI_FRAME_FIELD.Result] === partial)
    return original
  return { ...original, [OH_MY_PI_FRAME_FIELD.Result]: partial }
}
