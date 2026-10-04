import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { nativeContext, runningChild } from './scenarios'

commandCodeTest('follows an actual native child from running to completed', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  const child = await runningChild(context)
  await withCleanup(async () => {
    await expect(child.row).toHaveAttribute('data-kind', 'subagent')
    await expect(child.row).toHaveAttribute('data-status', 'running')
    expect(child.childId).not.toBe(child.parentId)
  }, child.finish)
  await expect(child.row).toHaveAttribute('data-status', 'completed')
})
