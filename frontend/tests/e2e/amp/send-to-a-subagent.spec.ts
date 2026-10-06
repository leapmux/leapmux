import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { tabById, waitForSettingsHydrated } from '../helpers/ui'
import { clickOpaqueAmpTaskRow, openOpaqueAmpTask } from './opaqueTask'

// Amp's stream carries the remote Task call and report without a child session ID.
ampTest('proves the actual opaque remote Task limit for send-to-a-subagent', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  const task = await openOpaqueAmpTask(context)
  try {
    const parent = await currentNativeAgent(context)
    expect(parent.acceptsMessages).toBe(true)
    expect((await readNativeSidebarSnapshot(context, parent.id)).backgroundTasks.every(row => row.childAgentId === '')).toBe(true)
    await expect(task.row).toHaveAttribute('aria-disabled', 'true')
    await clickOpaqueAmpTaskRow(task.row)
    await expect(tabById(page, parent.id)).toHaveAttribute('aria-selected', 'true')
    await expect(page.locator('[data-testid="composer-editor"]:visible .ProseMirror')).toHaveAttribute('contenteditable', 'true')
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
