import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { messageContents, waitForSettingsHydrated } from '../helpers/ui'
import { openOpaqueAmpTask } from './opaqueTask'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

// Amp's stream carries the remote Task call and report without a child session ID.
ampTest('proves the actual opaque remote Task limit for subagent-live-transcript', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  const task = await openOpaqueAmpTask(context)
  try {
    expect(JSON.stringify(task.childRequest.body)).toContain('AMPREMOTETASK')
    await expect(messageContents(page).filter({ hasText: task.progress })).toHaveCount(0)
    await expect(task.row).not.toContainText(task.progress)
    const snapshot = await readNativeSidebarSnapshot(context, task.parentId)
    expect(snapshot.backgroundTasks.every(row => row.childAgentId === '')).toBe(true)
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
