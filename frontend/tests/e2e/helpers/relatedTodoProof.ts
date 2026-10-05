import type { MockModelToolCall } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { TodoStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readNativeSidebarSnapshot } from './nativeSidebarSnapshot'
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

/**
 * Prove that the to-do list of `exerciseRelatedTodo` survives a reload.
 *
 * The Worker stores the native list, and the sidebar reads it again after the reload.
 * The scenario therefore requires the one pending item in the Worker snapshot and in the sidebar,
 * before and after the reload. The Background tasks section plays no part: a native to-do list is not a task row.
 */
export async function expectRelatedTodoSurvivesReload(context: ManagedNativeScenarioContext, item: string): Promise<void> {
  const expected = [{ content: item, status: TodoStatus.PENDING }]
  const storedTodos = async () => (await readNativeSidebarSnapshot(context)).todos.map(todo => ({ content: todo.content, status: todo.status }))
  expect(await storedTodos()).toEqual(expected)
  await context.page.reload()
  await expandGoalsAndTodosSection(context.page)
  const list = context.page.locator('[data-testid="goals-and-todos"]:visible').first()
  await expect(list).toContainText(item)
  await expect(list.locator('[data-task-checkbox]')).toHaveCount(1)
  await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(1)
  expect(await storedTodos()).toEqual(expected)
}
