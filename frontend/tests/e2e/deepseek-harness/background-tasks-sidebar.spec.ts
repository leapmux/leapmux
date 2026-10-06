import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { nativeContext, runningChild } from './scenarios'

deepseekHarnessTest('keeps the actual native child in the sidebar from running through completion', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  const child = await runningChild(context)
  await withCleanup(async () => {
    await expect(child.row).toHaveAttribute('data-kind', 'subagent')
    await expect(child.row).toHaveAttribute('data-status', 'running')
    expect(child.childId).not.toBe(child.parentId)
  }, child.finish)
  await expect(child.row).toHaveAttribute('data-status', 'completed')
  await page.reload()
  await expect(child.row).toHaveAttribute('data-status', 'completed')
})
