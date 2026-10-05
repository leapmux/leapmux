import type { NotificationEntry } from '../../../model/notification'
import { LETTA_DELTA_FIELD, LETTA_DELTA_KIND, LETTA_MESSAGE } from '~/generated/contracts/letta-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'

// Fields that only this reader needs, so no contract table holds them. The
// discriminator of a whole frame is `type`. Two fields of a `loop_error` delta
// state its words.
const FRAME_TYPE_FIELD = 'type'
const LOOP_ERROR_MESSAGE_FIELD = 'message'
const LOOP_ERROR_API_ERROR_FIELD = 'api_error'

/**
 * The words that Letta Code states for a loop error.
 *
 * `message` is the notice that Letta Code writes in its own transcript. It is
 * friendly text for a known failure (a lost connection, a service outage) and the
 * indented JSON of the error for a local backend error. `api_error` repeats the
 * service error in a structured form, so it answers when the notice has no words.
 * Both are empty when the delta states neither.
 */
function lettaLoopErrorWords(delta: Record<string, unknown>): string {
  return pickString(delta, LOOP_ERROR_MESSAGE_FIELD).trim()
    || pickString(pickObject(delta, LOOP_ERROR_API_ERROR_FIELD), LOOP_ERROR_MESSAGE_FIELD).trim()
}

/**
 * Read one notice that the Worker stored for Letta Code into the shared
 * notification model.
 *
 * The Worker stores a notice as Letta Code sent it. A loop error draws as the words
 * that Letta Code states for it, because the frame is a record of ids, dates and
 * flags around those words. The subagent snapshot is protocol state and draws
 * nothing: the Worker draws each child of it as the child's own rows and tab, and
 * Letta Code sends one snapshot at the start of every turn and another at each
 * state change of a child. Any other notice draws as its raw frame, so a frame that
 * this build does not know still reaches the reader.
 */
export function lettaNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  if (pickString(message, FRAME_TYPE_FIELD) === LETTA_MESSAGE.UpdateSubagentState)
    return []
  if (pickString(message, LETTA_DELTA_FIELD.MessageType) === LETTA_DELTA_KIND.LoopError) {
    const words = lettaLoopErrorWords(message)
    if (words)
      return [{ kind: 'text', text: words }]
  }
  return [{ kind: 'text', text: JSON.stringify(message) }]
}
