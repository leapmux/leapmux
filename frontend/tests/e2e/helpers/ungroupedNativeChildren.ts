import type { Locator } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { RunningNativeChild } from './unsupportedSubagent'
import { expect } from '@playwright/test'
import { BackgroundTaskKind } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from './cleanup'
import { readNativeSidebarSnapshot } from './nativeSidebarSnapshot'
import { expandBackgroundTasksSection } from './subagentRegistry'
import { workflowGroupHeading, workflowRowsShareGroup } from './workflowGrouping'

/** Inspect two actual native children and their authoritative group fields. */
export async function exerciseUngroupedNativeChildren(
  context: ManagedNativeScenarioContext,
  options: { openChild: (index: number) => Promise<RunningNativeChild>, nativeCatalogProof?: () => Promise<void> },
): Promise<void> {
  const children: Array<{ childId: string, parentId: string, row: Locator }> = []
  for (const index of [0, 1]) {
    const child = await options.openChild(index)
    await withCleanup(async () => {
      await expect(child.row).toHaveAttribute('data-status', 'running')
      await expect(child.row).toHaveAttribute('data-child-agent-id', child.childId)
      if (children.length > 0)
        expect(child.parentId).toBe(children[0]?.parentId)
      const response = await readNativeSidebarSnapshot(context, child.parentId)
      const task = response.backgroundTasks.find(item => item.childAgentId === child.childId)
      expect(task).toBeDefined()
      expect(task?.kind).toBe(BackgroundTaskKind.SUBAGENT)
      expect(task?.groupKey).toBe('')
      children.push({ childId: child.childId, parentId: child.parentId, row: child.row })
    }, child.finish)
  }
  await options.nativeCatalogProof?.()
  const inspect = async () => {
    await expandBackgroundTasksSection(context.page)
    const response = await readNativeSidebarSnapshot(context, children[0]?.parentId)
    for (const child of children) {
      const task = response.backgroundTasks.find(item => item.childAgentId === child.childId)
      expect(task?.kind).toBe(BackgroundTaskKind.SUBAGENT)
      expect(task?.groupKey).toBe('')
      expect(task?.groupLabel).toBe('')
      await expect(child.row).toHaveAttribute('data-status', 'completed')
      expect(await workflowGroupHeading(child.row)).toBe('')
    }
    const first = children[0]
    const second = children[1]
    if (!first || !second)
      throw new Error('The native grouping limit requires two actual child rows.')
    expect(first.childId).not.toBe(second.childId)
    expect(await workflowRowsShareGroup(first.row, second.row)).toBe(false)
    expect(response.backgroundTasks.some(item => item.kind === BackgroundTaskKind.WORKFLOW)).toBe(false)
  }
  await inspect()
  await context.page.reload()
  await inspect()
}
