import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { qoderTest } from '../qoder-fixtures'
import { runningChild } from './scenarios'

qoderTest('follows a native child from running to completed in the Background tasks sidebar', async ({ native }) => {
  const child = await runningChild(native)
  await withCleanup(async () => {
    await expect(child.row).toContainText('Native held child')
    await expect(child.row).toHaveAttribute('data-kind', 'subagent')
    await expect(child.row).toHaveAttribute('data-status', 'running')
    expect(child.childId).not.toBe(child.parentId)
  }, child.finish)
  await expect(child.row).toHaveAttribute('data-status', 'completed')
})
