import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { TodoStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expandGoalsAndTodosSection, goalsAndTodosList } from './goalsAndTodos'
import { readNativeSidebarSnapshot } from './nativeSidebarSnapshot'
import { updateTodosToolCall } from './providerToolCalls'
import { sendMessage, waitForAgentIdle } from './ui'

/** The ID of the native call that creates the related to-do item by default. */
export const RELATED_TODO_CALL_ID = 'related-native-todo'

/** The text of the related to-do item by default. */
export const RELATED_TODO_ITEM = 'Native capability proof'

/** The model's answer after the native call that creates the item. */
const RELATED_TODO_ANSWER = 'The native sidebar capability proof ended.'

/** What {@link exerciseRelatedTodo} scripts. Each field has a default. */
export interface RelatedTodoOptions {
  /**
   * The native call that creates the item. The default is the provider's update-todos call for `item`.
   * A provider whose vocabulary has no update-todos call, such as Pi, passes its own call, which must create `item`.
   */
  toolCall?: MockModelToolCall
  /** The item text that the sidebar must show. The default is {@link RELATED_TODO_ITEM}. */
  item?: string
  /** Answer in the step of the tool call, for a provider that runs the tool and answers in one model exchange. */
  singleRequest?: boolean
  /** Prepare the session before the turn, for a provider that asks before a tool runs. */
  prepare?: () => Promise<void>
}

/** The item and the ordered model steps of one related to-do turn. Pure, so a unit test can check the defaults. */
export function relatedTodoTurn(provider: AgentProvider, options: Omit<RelatedTodoOptions, 'prepare'> = {}): { item: string, steps: MockModelStep[] } {
  const item = options.item ?? RELATED_TODO_ITEM
  if (item.trim() === '')
    throw new Error('The related to-do item needs text.')
  const toolCall = options.toolCall ?? updateTodosToolCall(provider, RELATED_TODO_CALL_ID, [{ step: item, status: 'pending' }])
  const steps: MockModelStep[] = options.singleRequest
    ? [{ toolCalls: [toolCall], text: RELATED_TODO_ANSWER }]
    : [{ toolCalls: [toolCall] }, { text: RELATED_TODO_ANSWER }]
  return { item, steps }
}

/** Exercise a real sidebar update before an absent provider control is checked. */
export async function exerciseRelatedTodo(context: ManagedNativeScenarioContext, options: RelatedTodoOptions = {}): Promise<void> {
  await options.prepare?.()
  const turn = relatedTodoTurn(context.provider, options)
  const start = await context.modelScript.queue(...turn.steps)
  await sendMessage(context.page, context.modelScript.prompt('Create the scripted to-do item through the native tool.'))
  await context.modelScript.waitForSteps(start + turn.steps.length)
  await waitForAgentIdle(context.page)
  await expandGoalsAndTodosSection(context.page)
  const list = goalsAndTodosList(context.page)
  await expect(list).toContainText(turn.item)
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
  const list = goalsAndTodosList(context.page)
  await expect(list).toContainText(item)
  await expect(list.locator('[data-task-checkbox]')).toHaveCount(1)
  await expect(list.locator('[data-task-checkbox="pending"]')).toHaveCount(1)
  expect(await storedTodos()).toEqual(expected)
}
