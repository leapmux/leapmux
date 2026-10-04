import type { MockModelToolCall } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { expandGoalsAndTodosSection } from './subagentRegistry'
import { sendMessage, waitForAgentIdle } from './ui'

/** Exercise a real sidebar update before an absent provider control is checked. */
export async function exerciseRelatedTodo(
  context: ManagedNativeScenarioContext,
  options: { toolCall: MockModelToolCall, item: string, singleRequest?: boolean, prepare?: () => Promise<void> },
): Promise<void> {
  await options.prepare?.()
  const answer = 'The native sidebar capability proof ended.'
  if (options.singleRequest) {
    await context.modelScript.queue({ toolCalls: [options.toolCall], text: answer })
  }
  else {
    await context.modelScript.queue({ toolCalls: [options.toolCall] }, { text: answer })
  }
  await sendMessage(context.page, context.modelScript.prompt('Create the scripted to-do item through the native tool.'))
  await context.modelScript.waitForSteps()
  await waitForAgentIdle(context.page)
  await expandGoalsAndTodosSection(context.page)
  const list = context.page.locator('[data-testid="goals-and-todos"]:visible').first()
  await expect(list).toContainText(options.item)
  await expect(list.locator('[data-task-checkbox="pending"]')).not.toHaveCount(0)
}
