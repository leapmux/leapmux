import type { BackgroundTaskItem } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelMatcher, MockModelRule, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { RunningNativeChild } from './unsupportedSubagent'
import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { validateGateName } from './mockModelScript'
import { currentNativeAgent } from './nativeScenario'
import { readNativeSidebarSnapshot } from './nativeSidebarSnapshot'
import { expandBackgroundTasksSection, expectNoRegistryRows, expectRowBecomesFinal } from './subagentRegistry'
import { sendMessage, tabById, waitForAgentIdle, waitForControlBanner } from './ui'

export interface RunningChildOptions {
  spawn: MockModelToolCall
  gate: string
  childMatcher?: MockModelMatcher
  /** Match the actual result after the child tool runs. */
  childFinalMatcher?: MockModelMatcher
  childTool?: MockModelToolCall
  childFinalStep?: MockModelStep
  parentSteps?: readonly MockModelStep[]
  rules?: readonly MockModelRule[]
  singleRequest?: boolean
  approveSpawn?: boolean
  allowExistingRows?: boolean
  rowText?: string
  prepare?: () => Promise<void>
  /** Register provider-owned report handling before the held response can complete. */
  beforeRelease?: () => Promise<void>
  resolveTaskId?: (parentId: string) => Promise<string>
}

export interface NativeChildScriptContext {
  provider: ManagedNativeScenarioContext['provider']
  prompt: (text: string) => string
  textStep: (text: string) => MockModelStep
}

export type NativeChildTask = Pick<BackgroundTaskItem, 'id' | 'kind' | 'status' | 'childAgentId' | 'parentAgentId' | 'title'>

export interface NativeChildTaskSelection {
  parentId: string
  rootAgentId: string
  previousChildIds: ReadonlySet<string>
  rowText?: string
  taskId?: string
}

/** Select one exact running native child from the authoritative parent snapshot. */
export function selectRunningChildTask(tasks: readonly NativeChildTask[], options: NativeChildTaskSelection): NativeChildTask | undefined {
  if (!options.parentId || !options.rootAgentId || (options.taskId !== undefined && options.taskId.trim() === ''))
    throw new Error('The native child selection requires exact parent, root, and supplied task identities.')
  const matches = tasks.filter(task => task.kind === BackgroundTaskKind.SUBAGENT
    && task.status === BackgroundTaskStatus.RUNNING && task.id.trim() !== '' && task.childAgentId.trim() !== ''
    && (task.parentAgentId === options.parentId || (task.parentAgentId === '' && options.parentId === options.rootAgentId))
    && !options.previousChildIds.has(task.childAgentId)
    && (options.taskId === undefined || task.id === options.taskId)
    && (options.taskId !== undefined || options.rowText === undefined || task.title.includes(options.rowText)))
  if (matches.length > 1)
    throw new Error('The native child selection matched more than one running task.')
  return matches[0]
}

/** Keep each native child's rules distinct within the shared model scenario. */
export function nativeChildRuleId(gate: string, ruleId: string): string {
  validateGateName(gate)
  return `[${gate}] ${ruleId}`
}

/** Build the native rules without changing their provider-owned matchers or answers. */
export function runningNativeChildRules(options: RunningChildOptions): MockModelRule[] {
  if (options.childFinalMatcher && (!options.childMatcher || !options.childTool))
    throw new Error('A distinct child completion matcher requires a task matcher and an initial tool call.')
  const scopedId = (ruleId: string) => nativeChildRuleId(options.gate, ruleId)
  const rules = (options.rules ?? []).map(rule => ({ ...rule, name: scopedId(rule.name) }))
  if (options.childMatcher) {
    if (options.childTool)
      rules.push({ name: scopedId('the native child runs its actual tool'), when: options.childMatcher, respond: { toolCalls: [options.childTool] }, once: true })
    rules.push({ name: scopedId('the native child holds its final reply'), when: options.childFinalMatcher ?? options.childMatcher, respond: { ...(options.childFinalStep ?? { text: 'NATIVE_CHILD_FINAL_REPLY' }), gate: options.gate }, once: true })
  }
  return rules
}

/** Open a real child and hold its native completion until the caller finishes its proof. */
export async function openRunningNativeChild(
  context: ManagedNativeScenarioContext,
  options: RunningChildOptions,
): Promise<RunningNativeChild> {
  await options.prepare?.()
  const parent = await currentNativeAgent(context)
  if (!options.allowExistingRows)
    await expectNoRegistryRows(context.page, context.leapmuxServer)
  const previousChildIds = new Set((await readNativeSidebarSnapshot(context, parent.id)).backgroundTasks.map(task => task.childAgentId))
  if (options.parentSteps?.length === 0)
    throw new Error('The native child proof requires a parent step.')
  try {
    await context.modelScript.rule(...runningNativeChildRules(options))
    const parentSteps = options.parentSteps
      ?? (options.singleRequest
        ? [{ toolCalls: [options.spawn], text: 'The native parent received its child report.' }]
        : [{ toolCalls: [options.spawn] }, { text: 'The native parent received its child report.' }])
    const start = await context.modelScript.queue(...parentSteps)
    await sendMessage(context.page, context.modelScript.prompt('Create the scripted native child for its capability proof.'))
    if (options.approveSpawn) {
      await context.modelScript.waitForSteps(start + 1)
      await waitForControlBanner(context.page)
      await context.page.getByTestId('control-allow-btn').filter({ visible: true }).first().click()
    }
    await context.modelScript.waitForGate(options.gate)
    await options.beforeRelease?.()
    const taskId = await options.resolveTaskId?.(parent.id)
    let selectedChildId = ''
    await expect.poll(async () => {
      const snapshot = await readNativeSidebarSnapshot(context, parent.id)
      const selected = selectRunningChildTask(snapshot.backgroundTasks, {
        parentId: parent.id,
        rootAgentId: parent.rootAgentId,
        previousChildIds,
        ...(options.rowText !== undefined ? { rowText: options.rowText } : {}),
        ...(taskId !== undefined ? { taskId } : {}),
      })
      selectedChildId = selected?.childAgentId ?? ''
      return selectedChildId
    }).not.toBe('')
    await expandBackgroundTasksSection(context.page)
    const row = context.page.locator(`[data-testid="bg-task-row"]:visible[data-kind="subagent"][data-child-agent-id="${selectedChildId}"]`).first()
    await expect(row).toBeVisible()
    await expect(row).toHaveAttribute('data-status', 'running')
    let childId = ''
    await expect.poll(async () => {
      childId = await row.getAttribute('data-child-agent-id') ?? ''
      return childId
    }).not.toBe('')
    return {
      row,
      childId,
      parentId: parent.id,
      finish: async () => {
        await context.modelScript.releaseGateIfHeld(options.gate)
        await context.modelScript.waitForSteps(start + parentSteps.length)
        await tabById(context.page, parent.id).click()
        await waitForAgentIdle(context.page)
        await expectRowBecomesFinal(context.page, row)
      },
    }
  }
  catch (error) {
    try {
      await context.modelScript.releaseGateIfHeld(options.gate)
    }
    catch (cleanupError) {
      throw new AggregateError([error, cleanupError], 'The native child setup and its gate cleanup failed.')
    }
    throw error
  }
}
