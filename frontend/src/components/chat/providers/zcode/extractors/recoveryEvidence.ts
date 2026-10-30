import { ZCODE_METHOD, ZCODE_SCOPE, ZCODE_STATE_REASON } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { zcodeEnvelope } from './toolCommon'

/** Read the native notification that supports a recovered turn end. */
export function zcodeRecoveryEvidence(parsed: unknown): boolean {
  const envelope = zcodeEnvelope(parsed)
  if (envelope?.type !== ZCODE_METHOD.StateUpdated || pickString(envelope.payload, 'scope') !== ZCODE_SCOPE.Session)
    return false
  const reason = pickString(envelope.payload, 'reason')
  return reason === ZCODE_STATE_REASON.PromptCompleted || reason === ZCODE_STATE_REASON.PromptFailed
}
