import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModelTurn } from '../helpers/nativeScenario'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'

/**
 * A whole user row that holds one `<system-reminder>` block. Kimi Code injects
 * these rows itself, such as the date-change reminder it restates whenever the
 * day changes, so such a row is the model's input context, not a conversation
 * turn, and the reader returns no user turn for it.
 */
const NATIVE_CONTEXT_ROW = /^<system-reminder>\n[\s\S]*\n<\/system-reminder>$/

/**
 * Read the turns of a native Kimi Code request through the generic message
 * reader, with the engine's own reminder rows classified as context.
 */
export function kimiModelTurns(request: MockModelRequestRecord): NativeModelTurn[] {
  return nativeModelConversationTurns(request).filter(turn => turn.role !== 'user' || !NATIVE_CONTEXT_ROW.test(turn.text))
}
