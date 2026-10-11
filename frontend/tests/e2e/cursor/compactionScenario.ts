import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { exerciseCompactAsModelText } from '../helpers/unsupportedCompaction'

/**
 * Read the prompt of a recorded Cursor Run request.
 * The mock records the text of the new user message as `prompt` (`helpers/cursorSurface.ts`), and the service keeps
 * the earlier messages of the conversation as its own server context.
 */
export function cursorRunPrompt(request: MockModelRequestRecord): string {
  if (!isObject(request.body) || typeof request.body.prompt !== 'string')
    throw new Error('The Cursor Run request has no prompt text.')
  return request.body.prompt
}

/**
 * Prove that Cursor sends `/compact` to its service as prompt text in the same conversation, and that the service
 * keeps the earlier context and the native session for the next turn.
 */
export async function exerciseCursorCompactAsText(context: ManagedNativeScenarioContext): Promise<void> {
  const before = await currentNativeAgent(context)
  const { first, request, prompt, answer } = await exerciseCompactAsModelText(context, { lastUserText: cursorRunPrompt })
  expect(first.serverContext?.conversationId).toBeTruthy()
  expect(request.serverContext?.conversationId).toBe(first.serverContext?.conversationId)
  const next = await sendNativeAnswer(context, 'Continue after the unsupported compact command.', 'The original Cursor context remains.')
  expect(nativeModelContextText(next)).toContain(prompt)
  expect(nativeModelContextText(next)).toContain(answer)
  expect((await currentNativeAgent(context)).agentSessionId).toBe(before.agentSessionId)
}
