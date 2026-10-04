import { KIRO_KIND, KIRO_META } from '~/generated/contracts/kiro-protocol'
import { pickString } from '~/lib/jsonPick'
import { ACP_SESSION_UPDATE } from '../acp/updateVocabulary'
import { kiroMeta } from './protocol'

/**
 * The stop reason of a turn that Kiro started by itself, or undefined for any other
 * frame.
 *
 * Kiro starts a turn of its own when a workflow wakes the session, and it brackets
 * every turn with a `turn_start` and a `turn_end` in the `_meta.kiro` of a
 * `session_info_update`. The worker stores that `turn_end` update as the end of such
 * a turn, as it stores the prompt response of a turn LeapMux started.
 *
 * A `turn_end` that states no reason still ends the turn, so it answers the empty
 * reason, which the divider reads as a plain end.
 */
export function kiroAgentTurnEnd(parent: Record<string, unknown>): string | undefined {
  if (parent.sessionUpdate !== ACP_SESSION_UPDATE.SESSION_INFO_UPDATE)
    return undefined
  const kiro = kiroMeta(parent)
  if (pickString(kiro, KIRO_META.Kind) !== KIRO_KIND.TurnEnd)
    return undefined
  const reason = kiro?.[KIRO_META.StopReason]
  return typeof reason === 'string' ? reason : ''
}
