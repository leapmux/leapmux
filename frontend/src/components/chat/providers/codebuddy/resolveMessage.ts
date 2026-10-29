import type { ParsedMessageContent } from '~/lib/messageParser'
import { CODEBUDDY_FRAME_KIND, CODEBUDDY_RESULT_SUPPLEMENT_FIELD } from '~/generated/contracts/codebuddy-protocol'
import { isObject, pickString } from '~/lib/jsonPick'

/** Omit a repeated diagnostic from the display copy while preserving both native rows. */
export function resolveCodebuddyMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const frame = parsed.parentObject
  const extra = parsed.supplementalContent
  if (!frame || frame.type !== CODEBUDDY_FRAME_KIND.Result || frame.is_error !== true || !isObject(extra) || !Array.isArray(frame.errors))
    return undefined
  const requestID = pickString(extra, CODEBUDDY_RESULT_SUPPLEMENT_FIELD.RequestID)
  const sessionID = pickString(extra, CODEBUDDY_RESULT_SUPPLEMENT_FIELD.SessionID)
  const repeated = pickString(extra, CODEBUDDY_RESULT_SUPPLEMENT_FIELD.RepeatedError)
  if (!requestID.trim() || !sessionID.trim() || !repeated.trim()
    || frame._requestId !== requestID || frame.session_id !== sessionID || frame.errors[0] !== repeated) {
    return undefined
  }
  return {
    ...frame,
    errors: frame.errors.filter(error => error !== repeated),
  }
}
