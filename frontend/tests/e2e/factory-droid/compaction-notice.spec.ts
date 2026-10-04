import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { compactionNoticeRow } from '../helpers/compaction'
import { exerciseCompletedManualCompaction } from './compactionScenarios'
import { nativeContext } from './scenarios'

droidTest('keeps the completed native compaction status after reload', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseCompletedManualCompaction(context)
  await expect(compactionNoticeRow(page)).toBeVisible()
  await page.reload()
  await expect(compactionNoticeRow(page)).toBeVisible()
})
