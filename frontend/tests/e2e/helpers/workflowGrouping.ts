import type { Locator } from '@playwright/test'
import type { BackgroundTaskItem } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeSidebarContext } from './nativeSidebarSnapshot'
import type { RunningNativeChild } from './runningChildProof'
import { expect } from '@playwright/test'
import { BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from './cleanup'
import { nativeModelToolNames } from './nativeScenario'
import { readNativeSidebarSnapshot } from './nativeSidebarSnapshot'
import { WORKFLOW_TOOL_NAMES } from './providerToolCalls'
import { backgroundTaskRows, expandBackgroundTasksSection, expectRowBecomesFinal } from './subagentRegistry'

/** Find the group heading before a row, past other rows in that group. */
export function workflowGroupHeadingElement(row: Element): Element | null {
  let sibling = row.previousElementSibling
  while (sibling?.getAttribute('data-testid') === 'bg-task-row')
    sibling = sibling.previousElementSibling
  return sibling
}

/** Read the group heading above a background task row. */
export async function workflowGroupHeading(row: Locator): Promise<string> {
  const heading = await row.evaluateHandle(workflowGroupHeadingElement)
  try {
    return await heading.evaluate(element => element?.textContent?.trim() ?? '')
  }
  finally {
    await heading.dispose()
  }
}

/** Check that two rows follow the same group heading element. */
export async function workflowRowsShareGroup(first: Locator, second: Locator): Promise<boolean> {
  const firstHeading = await first.evaluateHandle(workflowGroupHeadingElement)
  try {
    const secondHeading = await second.evaluateHandle(workflowGroupHeadingElement)
    try {
      return await firstHeading.evaluate((element, other) => element !== null && element.isSameNode(other), secondHeading)
    }
    finally {
      await secondHeading.dispose()
    }
  }
  finally {
    await firstHeading.dispose()
  }
}

/**
 * Require every row of `rows` under one workflow group heading.
 * A string `heading` must equal the heading text, and a RegExp must match it. Each row must follow the same heading
 * element as the first row, so two groups with equal heading text fail.
 */
export async function expectRowsInWorkflowGroup(rows: readonly Locator[], heading: string | RegExp): Promise<void> {
  const [first, ...others] = rows
  if (!first || others.length === 0)
    throw new Error('A workflow group check needs two or more rows.')
  for (const row of rows) {
    if (typeof heading === 'string')
      await expect.poll(() => workflowGroupHeading(row), { message: 'the workflow group heading of the row' }).toBe(heading)
    else
      await expect.poll(() => workflowGroupHeading(row), { message: 'the workflow group heading of the row' }).toMatch(heading)
  }
  for (const row of others)
    await expect.poll(() => workflowRowsShareGroup(first, row), { message: 'the row follows the group heading of the first row' }).toBe(true)
}

/** Require an actual loaded registry without workflow rows or workflow grouping keys. Read the selected agent without `agentId`. */
export async function expectNoNativeWorkflowGroups(context: NativeSidebarContext, agentId?: string): Promise<void> {
  const snapshot = await readNativeSidebarSnapshot(context, agentId)
  expect(snapshot.backgroundTasks.filter(task => task.kind === BackgroundTaskKind.WORKFLOW)).toEqual([])
  expect(snapshot.backgroundTasks.filter(task => task.groupKey !== '' || task.groupLabel !== '')).toEqual([])
  await expect(backgroundTaskRows(context.page, { kind: 'workflow' })).toHaveCount(0)
}

/**
 * Prove real native assignments inside one opaque workflow row, without separate child rows, before and after a reload.
 * `ruleNames` states the rule of each assignment. Each rule must answer exactly one request.
 */
export async function expectOpaqueNativeWorkflowResult(
  context: ManagedNativeScenarioContext,
  options: { ruleNames: readonly string[], heading: string | RegExp },
): Promise<void> {
  if (options.ruleNames.length === 0 || new Set(options.ruleNames).size !== options.ruleNames.length)
    throw new Error('The native workflow proof requires one or more distinct assignment rules.')
  await expandBackgroundTasksSection(context.page)
  const workflow = backgroundTaskRows(context.page, { kind: 'workflow' }).first()
  await expectRowBecomesFinal(context.page, workflow)
  await expect(workflow).toHaveAttribute('data-status', 'completed')
  const status = await context.modelScript.status()
  for (const rule of options.ruleNames) {
    expect(status.ruleMatches[rule]).toBe(1)
    expect(status.requests.filter(request => request.rule === rule)).toHaveLength(1)
  }
  const proveRegistry = async () => {
    const snapshot = await readNativeSidebarSnapshot(context)
    expect(snapshot.backgroundTasks.filter(task => task.kind === BackgroundTaskKind.WORKFLOW)).toHaveLength(1)
    expect(snapshot.backgroundTasks.filter(task => task.kind === BackgroundTaskKind.SUBAGENT)).toEqual([])
    await expect(backgroundTaskRows(context.page)).toHaveCount(1)
    await expect(backgroundTaskRows(context.page, { kind: 'subagent' })).toHaveCount(0)
    if (typeof options.heading === 'string')
      await expect.poll(() => workflowGroupHeading(workflow)).toBe(options.heading)
    else
      await expect.poll(() => workflowGroupHeading(workflow)).toMatch(options.heading)
  }
  await proveRegistry()
  await context.page.reload()
  await expandBackgroundTasksSection(context.page)
  await expect(workflow).toHaveAttribute('data-status', 'completed')
  await proveRegistry()
}

/**
 * Require that the first ordered request of the scenario offers no workflow tool of any provider
 * (`WORKFLOW_TOOL_NAMES` of `./providerToolCalls.ts`).
 * In a scenario that starts by spawning a child, that request is the spawn request of the parent.
 */
export async function expectNoWorkflowToolOffered(modelScript: Pick<ModelScript, 'status'>): Promise<void> {
  const parentRequest = (await modelScript.status()).requests.find(record => record.stepIndex !== undefined)
  if (!parentRequest)
    throw new Error('The native child sequence contains no parent model request.')
  const offered = nativeModelToolNames(parentRequest).filter(name => WORKFLOW_TOOL_NAMES.includes(name))
  expect(offered, 'the parent request offers no workflow tool').toEqual([])
}

/**
 * The child agent IDs that two ungrouped subagent tasks must hold:
 *
 * - `absent`: no task has a child agent, as an opaque remote task has none.
 * - `distinct`: each task has a child agent of its own.
 * - Two IDs: the tasks hold exactly these child agents.
 */
export type UngroupedChildAgentIds = 'absent' | 'distinct' | readonly [string, string]

/** The fields of a Worker task that the ungrouped check reads. */
export type UngroupedChildTask = Pick<BackgroundTaskItem, 'kind' | 'status' | 'childAgentId' | 'groupKey' | 'groupLabel'>

/**
 * State why the Worker tasks are not two completed, ungrouped subagent tasks with the child agents of `childAgentIds`,
 * or return '' when they are.
 */
export function ungroupedChildTaskProblem(tasks: readonly UngroupedChildTask[], childAgentIds: UngroupedChildAgentIds): string {
  if (tasks.some(task => task.kind === BackgroundTaskKind.WORKFLOW))
    return 'the registry holds a workflow task'
  const grouped = tasks.filter(task => task.groupKey !== '' || task.groupLabel !== '')
  if (grouped.length > 0)
    return `${grouped.length} task(s) carry a workflow group key or label`
  const children = tasks.filter(task => task.kind === BackgroundTaskKind.SUBAGENT)
  if (children.length !== 2)
    return `the registry holds ${children.length} subagent task(s), not 2`
  if (children.some(task => task.status !== BackgroundTaskStatus.COMPLETED))
    return 'a subagent task is not completed'
  const ids = children.map(task => task.childAgentId)
  if (childAgentIds === 'absent')
    return ids.every(id => id === '') ? '' : `a subagent task holds a child agent: ${JSON.stringify(ids)}`
  if (ids.some(id => id.trim() === ''))
    return 'a subagent task holds no child agent'
  if (new Set(ids).size !== 2)
    return `the two subagent tasks hold one child agent: ${JSON.stringify(ids)}`
  if (childAgentIds !== 'distinct' && [...ids].sort().join('\n') !== [...childAgentIds].sort().join('\n'))
    return `the subagent tasks hold ${JSON.stringify(ids)}, not ${JSON.stringify(childAgentIds)}`
  return ''
}

/**
 * Require two completed subagent rows without workflow grouping, in the Worker snapshot and in the DOM, before and
 * after a reload. `parentId` selects the agent whose snapshot holds the tasks, and the selected agent is the default.
 */
export async function expectUngroupedChildRows(
  context: NativeSidebarContext,
  options: { childAgentIds: UngroupedChildAgentIds, parentId?: string },
): Promise<void> {
  const rows = backgroundTaskRows(context.page, { kind: 'subagent' })
  const inspect = async () => {
    const snapshot = await readNativeSidebarSnapshot(context, options.parentId)
    expect(ungroupedChildTaskProblem(snapshot.backgroundTasks, options.childAgentIds), 'the Worker holds two ungrouped child tasks').toBe('')
    await expectNoNativeWorkflowGroups(context, options.parentId)
    await expandBackgroundTasksSection(context.page)
    await expect(rows).toHaveCount(2)
    const pair = [rows.nth(0), rows.nth(1)] as const
    for (const row of pair) {
      await expect(row).toHaveAttribute('data-status', 'completed')
      expect(await workflowGroupHeading(row), 'an ungrouped row has no group heading').toBe('')
    }
    expect(await workflowRowsShareGroup(pair[0], pair[1]), 'the two rows share no group heading').toBe(false)
    if (typeof options.childAgentIds !== 'string') {
      const rowIds = await rows.evaluateAll(elements => elements.map(element => element.getAttribute('data-child-agent-id') ?? ''))
      expect([...rowIds].sort(), 'the rows link the two child agents').toEqual([...options.childAgentIds].sort())
    }
  }
  await inspect()
  await context.page.reload()
  await inspect()
}

/** One child of {@link exerciseUngroupedNativeChildren}: its place, and whether the rows of earlier children may exist. */
export interface UngroupedChildSlot {
  index: 0 | 1
  allowExistingRows: boolean
}

/**
 * Open two actual native children one after the other, and require that the registry keeps them ungrouped.
 *
 * - While each child runs: its row is running and links its agent, and its Worker task has no group key or label.
 * - While the second child runs: the first row is completed, the DOM holds two subagent rows, and no workflow group
 *   exists. Both children have one parent and distinct agents.
 * - After both finish: {@link expectUngroupedChildRows} for the two child agents, before and after a reload.
 *
 * `catalogProof` also requires that the parent's spawn request offers no workflow tool. Leave it out for a provider
 * whose request holds no readable tool catalog, such as the protobuf request of Cursor.
 */
export async function exerciseUngroupedNativeChildren(
  context: ManagedNativeScenarioContext,
  options: { openChild: (slot: UngroupedChildSlot) => Promise<RunningNativeChild>, catalogProof?: boolean },
): Promise<void> {
  const first = await options.openChild({ index: 0, allowExistingRows: false })
  await withCleanup(() => expectRunningUngroupedChild(context, first), first.finish)
  const second = await options.openChild({ index: 1, allowExistingRows: true })
  await withCleanup(async () => {
    expect(second.parentId, 'both children belong to one parent').toBe(first.parentId)
    expect(second.childId, 'the two children are distinct agents').not.toBe(first.childId)
    await expectRunningUngroupedChild(context, second)
    await expect(first.row).toHaveAttribute('data-status', 'completed')
    await expect(backgroundTaskRows(context.page, { kind: 'subagent' })).toHaveCount(2)
    await expectNoNativeWorkflowGroups(context, first.parentId)
  }, second.finish)
  if (options.catalogProof)
    await expectNoWorkflowToolOffered(context.modelScript)
  await expectUngroupedChildRows(context, { childAgentIds: [first.childId, second.childId], parentId: first.parentId })
}

/** Require a running child row that links its agent, and a Worker task of that child without a workflow group. */
async function expectRunningUngroupedChild(context: ManagedNativeScenarioContext, child: RunningNativeChild): Promise<void> {
  await expect(child.row).toHaveAttribute('data-status', 'running')
  await expect(child.row).toHaveAttribute('data-child-agent-id', child.childId)
  const task = (await readNativeSidebarSnapshot(context, child.parentId)).backgroundTasks.find(item => item.childAgentId === child.childId)
  expect(task, 'the Worker holds the task of the running child').toBeDefined()
  expect(task?.kind).toBe(BackgroundTaskKind.SUBAGENT)
  expect(task?.groupKey).toBe('')
  expect(task?.groupLabel).toBe('')
}
