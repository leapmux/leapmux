import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { lettaTest } from '../letta-fixtures'
import { nativeContext, runningChild } from './scenarios'

lettaTest('follows a native child from running to completed in the Background tasks sidebar', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  const child = await runningChild(context)
  await withCleanup(async () => {
    await expect(child.row).toContainText('Native held child')
    await expect(child.row).toHaveAttribute('data-kind', 'subagent')
    await expect(child.row).toHaveAttribute('data-status', 'running')
    expect(child.childId).not.toBe(child.parentId)
  }, child.finish)
  await expect(child.row).toHaveAttribute('data-status', 'completed')
})
