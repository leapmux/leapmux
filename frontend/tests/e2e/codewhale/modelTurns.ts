import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { codewhaleRequestMessages, isCodewhaleRuntimeContext } from './runtimeContext'

/** Read user conversation turns without treating native runtime receipts as user input. */
export function codewhaleModelTurns(request: MockModelRequestRecord) {
  if (request.protocol !== 'openai-chat-completions' || !isObject(request.body) || !Array.isArray(request.body.messages))
    return nativeModelConversationTurns(request)
  const messages: unknown[] = []
  for (const { message, content } of codewhaleRequestMessages(request.body.messages)) {
    if (!isObject(message) || message.role !== 'user' || !isCodewhaleRuntimeContext(content))
      messages.push(message)
  }
  return nativeModelConversationTurns({
    ...request,
    body: { ...request.body, messages },
  })
}
