import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModelTurn } from '../helpers/nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'

/**
 * Read the Cursor conversation that the service holds, then the current prompt. Cursor sends only the current prompt
 * and a conversation ID, so the service history is the model context of the request.
 */
export function cursorModelTurns(request: MockModelRequestRecord): NativeModelTurn[] {
  if (!isObject(request.body) || typeof request.body.prompt !== 'string')
    throw new Error('The Cursor turn reader requires the prompt of its native Run request.')
  const history = request.serverContext?.messages ?? []
  return [...history.map(message => ({ role: message.role, text: message.content })), { role: 'user', text: request.body.prompt }]
}
