import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModelTurn } from '../helpers/nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeToolArgumentText } from '../helpers/nativeScenario'

/**
 * Read the turns of a native Kiro request in request order, with the current prompt last.
 *
 * Kiro states its system prompt as the first user entry of the history. That entry stays a user turn here, because the
 * history gives no other role for it.
 */
export function kiroModelTurns(request: MockModelRequestRecord): NativeModelTurn[] {
  if (request.protocol !== 'aws-event-stream')
    throw new Error('The Kiro turn reader requires its native event-stream request.')
  const state = isObject(request.body) && isObject(request.body.conversationState) ? request.body.conversationState : undefined
  if (!state)
    throw new Error('The native Kiro request contains no conversation state.')
  const history = state.history ?? []
  if (!Array.isArray(history))
    throw new Error('The native Kiro conversation history is not an array.')
  const turns = history.flatMap((entry: unknown): NativeModelTurn[] => {
    if (!isObject(entry))
      return []
    if (isObject(entry.userInputMessage))
      return [{ role: 'user', text: typeof entry.userInputMessage.content === 'string' ? entry.userInputMessage.content : '' }]
    if (!isObject(entry.assistantResponseMessage))
      return []
    const message = entry.assistantResponseMessage
    const toolInputs = Array.isArray(message.toolUses)
      ? message.toolUses.filter(isObject).map(use => nativeToolArgumentText(use.input))
      : []
    const text = [typeof message.content === 'string' ? message.content : '', ...toolInputs].filter(part => part !== '').join('\n')
    return [{ role: 'assistant', text }]
  })
  const current = isObject(state.currentMessage) && isObject(state.currentMessage.userInputMessage) ? state.currentMessage.userInputMessage : undefined
  if (!current || typeof current.content !== 'string')
    throw new Error('The native Kiro request contains no current user message.')
  return [...turns, { role: 'user', text: current.content }]
}
