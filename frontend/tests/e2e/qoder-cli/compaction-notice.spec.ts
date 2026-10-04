import { expect } from '@playwright/test'
import { compactionNoticeRow } from '../helpers/compaction'
import { qoderTest } from '../qoder-fixtures'
import { exerciseCompletedManualCompaction } from './compactionScenarios'
import { nativeContext } from './scenarios'

qoderTest('keeps the completed native compaction status after reload', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  await exerciseCompletedManualCompaction(context)
  await expect(compactionNoticeRow(page)).toBeVisible()
  await page.reload()
  await expect(compactionNoticeRow(page)).toBeVisible()
})
