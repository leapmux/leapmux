import { MUSE_SUPPLEMENT_FIELD } from '~/generated/contracts/muse-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** Refused projection evidence keeps its native bytes without becoming a row. */
export function museViewProjectionUnavailable(payload: unknown, supplemental: unknown): boolean {
  const failure = pickObject(isObject(supplemental) ? supplemental : undefined, MUSE_SUPPLEMENT_FIELD.ViewProjectionUnavailable)
  const sessionId = pickString(failure, 'sessionId')
  const params = pickObject(isObject(payload) ? payload : undefined, 'params')
  return sessionId.trim() !== '' && pickString(params, 'sessionId') === sessionId && pickString(failure, 'reason').trim() !== ''
}
