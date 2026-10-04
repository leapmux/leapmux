import type { Locator } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeSidebarContext } from './nativeSidebarSnapshot'
import type { RunningNativeChild } from './unsupportedSubagent'
import { expect } from '@playwright/test'
import { BackgroundTaskKind } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from './cleanup'
import { readNativeSidebarSnapshot } from './nativeSidebarSnapshot'
import { expandBackgroundTasksSection, expectRowBecomesFinal } from './subagentRegistry'

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

/** Require an actual loaded registry without workflow rows or workflow grouping keys. */
export async function expectNoNativeWorkflowGroups(context: NativeSidebarContext): Promise<void> {
  const snapshot = await readNativeSidebarSnapshot(context)
  expect(snapshot.backgroundTasks.filter(task => task.kind === BackgroundTaskKind.WORKFLOW)).toEqual([])
  expect(snapshot.backgroundTasks.filter(task => task.groupKey !== '' || task.groupLabel !== '')).toEqual([])
  await expect(context.page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]')).toHaveCount(0)
}

/** Prove two real native assignments without separate child rows in the stored workflow. */
export async function expectOpaqueNativeWorkflowResult(
  context: ManagedNativeScenarioContext,
  options: { ruleNames: readonly string[], heading: string | RegExp },
): Promise<void> {
  if (options.ruleNames.length < 2 || new Set(options.ruleNames).size !== options.ruleNames.length)
    throw new Error('The native workflow proof requires two distinct assignment rules.')
  await expandBackgroundTasksSection(context.page)
  const workflow = context.page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]').first()
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
    await expect(context.page.locator('[data-testid="bg-task-row"]:visible')).toHaveCount(1)
    await expect(context.page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]')).toHaveCount(0)
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

/** Keep two actual native child rows independent before and after reload. */
export async function exerciseUngroupedNativeChildren(
  context: ManagedNativeScenarioContext,
  openChild: (options: { allowExistingRows: boolean }) => Promise<RunningNativeChild>,
): Promise<void> {
  const first = await openChild({ allowExistingRows: false })
  await first.finish()
  const second = await openChild({ allowExistingRows: true })
  await withCleanup(async () => {
    expect(first.childId).not.toBe(second.childId)
    await expect(first.row).toHaveAttribute('data-status', 'completed')
    await expect(second.row).toHaveAttribute('data-status', 'running')
    await expect(context.page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]')).toHaveCount(2)
    await expectNoNativeWorkflowGroups(context)
  }, second.finish)
  await context.page.reload()
  await expect(context.page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]')).toHaveCount(2)
  await expectNoNativeWorkflowGroups(context)
}
