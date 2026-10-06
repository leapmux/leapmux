import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ohMyPiYieldToolCall } from '../helpers/providerToolCalls'
import { backgroundTasksSection, expectRowBecomesFinal, expectSectionPersists, HELD_CHILD_NAME, HELD_CHILD_REPORT, HELD_CHILD_TASK, openHeldChildTab } from '../helpers/subagentRegistry'
import { applyPermissionPreset, tabById } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('keeps the actual native background task row through completion and reload', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await applyPermissionPreset(page, 'bypass')
  // A task item of omp has no description field. With no generated label, omp's own
  // task view shows the subagent ID and a summary of the assignment. The registry
  // row shows the subagent ID and the first line of the assignment.
  const child = await openHeldChildTab(context, { rowTitle: HELD_CHILD_NAME, childTurn: { user: HELD_CHILD_TASK }, heldAnswer: { toolCalls: [ohMyPiYieldToolCall('held-child-yield', HELD_CHILD_REPORT)] }, rootTurnsAfterSpawn: [{ text: 'The actual native child completed.' }] })
  try {
    await tabById(page, child.parentId).click()
    await expect(backgroundTasksSection(page)).toBeVisible()
    await expect(child.row).toHaveAttribute('data-status', 'running')
    await expect(child.row).toContainText(HELD_CHILD_NAME)
    await expect(child.row).toContainText(HELD_CHILD_TASK)
  }
  finally {
    await child.finish()
  }
  await expectRowBecomesFinal(page, child.row)
  await expectSectionPersists(page)
})
