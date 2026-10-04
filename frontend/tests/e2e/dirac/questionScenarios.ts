import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { askUserQuestionToolCall, diracRespondToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

/** Return only the actual native result for the question that the browser answers. */
export async function exerciseQuestionReply(
  context: ManagedNativeScenarioContext,
  reply: (page: Page) => Promise<void>,
): Promise<string> {
  const start = (await context.modelScript.status()).stepCount
  const callId = `dirac-question-${randomUUID()}`
  await context.modelScript.queue(
    { toolCalls: [askUserQuestionToolCall(context.provider, callId, [{
      question: 'Which color should I use?',
      header: 'Color',
      options: [{ label: 'Blue', description: 'Use blue.' }, { label: 'Red', description: 'Use red.' }],
    }])] },
    { toolCalls: [diracRespondToolCall('dirac-question-final', 'complete', 'The native question turn completed.')] },
  )
  await sendMessage(context.page, context.modelScript.prompt('Ask the scripted color question and then complete.'))
  await context.modelScript.waitForSteps(start + 1)
  await expect(context.page.locator('[data-testid="elicitation-form"]:visible')).toBeVisible()
  await reply(context.page)
  const status = await context.modelScript.waitForSteps(start + 2)
  const result = nativeToolResult(status.requests.find(record => record.stepIndex === start + 1), callId)
  await waitForAgentIdle(context.page)
  await expect(context.page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
  await expect(assistantBubbles(context.page).filter({ hasText: 'The native question turn completed.' }).first()).toBeVisible()
  return result
}
