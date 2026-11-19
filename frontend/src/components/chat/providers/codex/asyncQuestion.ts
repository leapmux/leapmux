import type { ControlResponseSummary } from '../../model/controlResponse'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { CONTROL_ANSWER_BEHAVIOR } from '~/generated/contracts/control-answer'
import { pickObject, pickString } from '~/lib/jsonPick'
import { decodeControlBehaviorEnvelope } from '~/utils/controlResponse'
import { label, labelOrNull } from '../../persistedControlResponse'

/**
 * The persisted display of one answered or skipped async question: the quoted
 * title with the answer, or the title under "Skipped". Any other answer gives
 * null, so the caller reads it another way.
 *
 * The recorded prompt states a question, and its one question is the title. The
 * prompt kind cannot tell LeapMux's own async question from Codex's
 * `item/tool/requestUserInput`, because both are questions, so the shape of the
 * answer does: the Worker answers an async question in the neutral envelope
 * (`codexAsyncQuestionAnswer` in Go), and requestUserInput with a JSON-RPC result.
 */
export function codexAsyncQuestionSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  if (cr.prompt?.kind !== 'question')
    return null
  // The Worker reads no question from an async question with no title, which a
  // reader can only skip.
  const title = cr.prompt.questions[0]?.question ?? ''
  // The stored row keeps the neutral envelope of the answer, whose behavior and
  // answer live at its innermost `response` (`{response: {response: {...}}}`).
  const behavior = decodeControlBehaviorEnvelope(cr.response)?.behavior
  if (behavior === CONTROL_ANSWER_BEHAVIOR.Deny) {
    return label(title
      ? `Skipped: ${title}`
      : 'Skipped')
  }
  if (behavior === CONTROL_ANSWER_BEHAVIOR.Allow) {
    const answer = pickString(pickObject(pickObject(cr.response, 'response'), 'response'), 'answer')
    return title
      ? label(`${title}: ${answer}`)
      : labelOrNull(answer)
  }
  return null
}
