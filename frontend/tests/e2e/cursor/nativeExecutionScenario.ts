import type { MockModelToolCall } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { approveNativeToolsUntil } from '../helpers/nativeToolExecution'
import { messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'

/**
 * Run native client operations within one actual Cursor Run.
 * The Cursor service streams the tool calls and the answer text in one Run response, so the turn uses one model step.
 * The operations end when the transcript shows `marker`.
 */
export async function runCursorNativeOperations(context: ManagedNativeScenarioContext, calls: MockModelToolCall[], marker: string): Promise<void> {
  const start = await context.modelScript.queue({ toolCalls: calls, text: 'The actual native operations completed.' })
  await sendMessage(context.page, context.modelScript.prompt('Execute the supplied native operations in this turn.'))
  await context.modelScript.waitForSteps(start + 1)
  const done = messageContents(context.page).filter({ hasText: marker }).first()
  await approveNativeToolsUntil(context.page, () => done.isVisible())
  await waitForAgentIdle(context.page)
  await expect(done).toBeVisible()
}

/** Read an actual stored native tool result by its exact call ID. */
export async function cursorNativeToolOutput(context: ManagedNativeScenarioContext, callId: string): Promise<Record<string, unknown>> {
  const agent = await currentNativeAgent(context)
  const snapshot = await readNativeMessageSnapshot(context, agent.id)
  if (snapshot.agentSessionId.trim() === '')
    throw new Error('The native Cursor tool result requires a nonempty session ID.')
  const results = snapshot.messages.filter(message => message.agentSessionId === snapshot.agentSessionId && message.spanId === callId).flatMap((message) => {
    const parsed = nativeMessageBody(message)
    if (!isObject(parsed) || parsed.toolCallId !== callId || parsed.status !== 'completed' || !isObject(parsed.rawOutput))
      return []
    return [parsed.rawOutput]
  })
  expect(results).toHaveLength(1)
  const output = results[0]
  if (!output)
    throw new Error('The actual native Cursor tool returned no stored output.')
  return output
}
