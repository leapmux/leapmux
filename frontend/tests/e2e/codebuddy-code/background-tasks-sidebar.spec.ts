import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { nativeContext, runningChild } from './scenarios'

codebuddyTest('follows a native child from running to completed in the Background tasks sidebar', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  const child = await runningChild(context)
  await withCleanup(async () => {
    await expect(child.row).toContainText('Native held child')
    await expect(child.row).toHaveAttribute('data-kind', 'subagent')
    await expect(child.row).toHaveAttribute('data-status', 'running')
    expect(child.childId).not.toBe(child.parentId)
  }, child.finish)
  await expect(child.row).toHaveAttribute('data-status', 'completed')
})
