/**
 * The to-do list replacement scenario of the Goals & To-dos section:
 *
 * - The agent writes a list.
 * - The agent writes the list again with every step completed.
 * - The sidebar follows each list and keeps the last one after a reload.
 */
import type { Locator } from '@playwright/test'
import type { MockModelStep } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import type { TodoStep } from './providerToolCalls'
import { expect } from '@playwright/test'
import { expandGoalsAndTodosSection, goalsAndTodosList, goalsAndTodosSection } from './goalsAndTodos'
import { nativeTextStep } from './nativeScenario'
import { updateTodosToolCall } from './providerToolCalls'
import { sendMessage, waitForAgentIdle } from './ui'

/** The steps of the list by default. */
export const TODO_LIST_STEPS: readonly string[] = ['Inspect the repository', 'List three checks', 'Report their purpose']

/** Each status that a to-do item can hold, in the order of a list's progress. */
const TODO_STATUSES: readonly TodoStep['status'][] = ['completed', 'in_progress', 'pending']

/**
 * Build the first list of the scenario:
 *
 * - The first step is completed.
 * - The last step is pending.
 * - Each other step is in progress.
 *
 * So the list holds a completed and a pending item, and the second list changes the status of each step after the
 * first.
 */
export function initialTodoList(steps: readonly string[]): TodoStep[] {
  if (steps.length < 2)
    throw new Error('A replaced to-do list needs two or more steps: the first starts completed, and the last starts pending.')
  if (steps.some(step => step.trim() === ''))
    throw new Error('Each to-do step needs text.')
  if (new Set(steps).size !== steps.length)
    throw new Error('Each to-do step needs its own text, because the sidebar check finds a step by its text.')
  return steps.map((step, index) => ({ step, status: index === 0 ? 'completed' : index === steps.length - 1 ? 'pending' : 'in_progress' }))
}

/** Build the second list of the scenario: the same steps, each completed. */
export function completedTodoList(steps: readonly string[]): TodoStep[] {
  return initialTodoList(steps).map(item => ({ ...item, status: 'completed' }))
}

/**
 * Return the first step that a write changes: a new step, or a step whose status differs from the previous list.
 * A permission banner of the write shows that step.
 */
export function firstChangedTodoStep(previous: readonly TodoStep[], next: readonly TodoStep[]): string {
  const changed = next.find(item => !previous.some(earlier => earlier.step === item.step && earlier.status === item.status))
  if (!changed)
    throw new Error('The to-do write changes no step.')
  return changed.step
}

/** What {@link exerciseTodoListReplacement} runs for one provider. Each field has a default. */
export interface TodoListReplacementOptions {
  /** The steps of the list. The default is {@link TODO_LIST_STEPS}. */
  steps?: readonly string[]
  /** Answer in the step of the tool call, for a provider that runs the tool and answers in one model exchange. */
  singleRequest?: boolean
  /**
   * Answer the permission request of each write, for a provider that asks before its to-do tool runs. The scenario
   * calls it after the agent requested the tool step, with the first step that the write changes.
   */
  approveWrite?: (changedStep: string) => Promise<void>
  /** Check provider-owned view state after the sidebar shows the first list, such as the transcript row of the write. */
  afterFirstList?: () => Promise<void>
}

/** Require the count of the list items in each status. */
async function expectTodoStatuses(list: Locator, items: readonly TodoStep[]): Promise<void> {
  for (const status of TODO_STATUSES) {
    const count = items.filter(item => item.status === status).length
    await expect(list.locator(`[data-task-checkbox="${status}"]`), `the to-do list holds ${count} ${status} items`).toHaveCount(count)
  }
}

/** Run one native turn that writes `items` as the whole to-do list. */
async function writeTodoList(
  context: NativeScenarioContext,
  options: TodoListReplacementOptions,
  write: { callId: string, items: TodoStep[], changedStep: string, prompt: string, answer: string },
): Promise<void> {
  const call = updateTodosToolCall(context.provider, write.callId, write.items)
  const answer = nativeTextStep(context, write.answer)
  const steps: MockModelStep[] = options.singleRequest
    ? [{ ...answer, toolCalls: [call, ...(answer.toolCalls ?? [])] }]
    : [{ toolCalls: [call] }, answer]
  const start = await context.modelScript.queue(...steps)
  await sendMessage(context.page, context.modelScript.prompt(write.prompt))
  if (options.approveWrite) {
    await context.modelScript.waitForSteps(start + 1)
    await options.approveWrite(write.changedStep)
  }
  await context.modelScript.waitForSteps(start + steps.length)
  await waitForAgentIdle(context.page)
}

/**
 * Prove that the sidebar follows each to-do list that the agent writes, and keeps the last list after a reload.
 * The agent writes the list of {@link initialTodoList}, then the same list with each item completed. A list of two
 * steps holds no item in progress. The native tool sends the whole list each time, so the second write replaces the
 * first.
 */
export async function exerciseTodoListReplacement(context: NativeScenarioContext, options: TodoListReplacementOptions = {}): Promise<void> {
  const steps = options.steps ?? TODO_LIST_STEPS
  const first = initialTodoList(steps)
  const done = completedTodoList(steps)
  await writeTodoList(context, options, { callId: 'todos-first', items: first, changedStep: firstChangedTodoStep([], first), prompt: `Write a ${steps.length}-step to-do list.`, answer: 'The plan is written.' })

  await expect(goalsAndTodosSection(context.page)).toBeVisible()
  await expandGoalsAndTodosSection(context.page)
  const list = goalsAndTodosList(context.page)
  for (const step of steps)
    await expect(list).toContainText(step)
  await expectTodoStatuses(list, first)
  await options.afterFirstList?.()

  await writeTodoList(context, options, { callId: 'todos-second', items: done, changedStep: firstChangedTodoStep(first, done), prompt: 'Mark every step done.', answer: 'Every step is done.' })
  await expectTodoStatuses(list, done)

  await context.page.reload()
  await expandGoalsAndTodosSection(context.page)
  await expectTodoStatuses(list, done)
}
