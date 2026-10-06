import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { tabById, waitForSettingsHydrated } from '../helpers/ui'
import { clickOpaqueAmpTaskRow, openOpaqueAmpTask } from './opaqueTask'

// Amp's stream carries the remote Task call and report without a child session ID.
ampTest('proves the actual opaque remote Task limit for subagent-transcript-tab', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  const task = await openOpaqueAmpTask(context)
  try {
    const tabs = page.locator('[data-testid="tab"][data-tab-type="agent"]:visible')
    const before = await tabs.count()
    await expect(task.row).toHaveAttribute('aria-disabled', 'true')
    await clickOpaqueAmpTaskRow(task.row)
    await expect(tabs).toHaveCount(before)
    await expect(tabById(page, task.parentId)).toHaveAttribute('aria-selected', 'true')
    await expect(task.row).toHaveAttribute('data-status', 'running')
  }
  finally {
    await task.finish()
  }
  await page.reload()
  await waitForSettingsHydrated(page, 'permissionMode')
  const saved = await readNativeSidebarSnapshot(context, task.parentId)
  expect(saved.backgroundTasks).toHaveLength(1)
  expect(saved.backgroundTasks[0]?.childAgentId).toBe('')
})
