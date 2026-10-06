import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { compactionNoticeRow } from '../helpers/compaction'
import { exerciseContextCompactionWithoutNotice } from './compactionScenarios'
import { nativeContext } from './scenarios'

codebuddyTest('receives no completed compaction notice from the actual native command', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  await exerciseContextCompactionWithoutNotice(context)
  await expect(compactionNoticeRow(page)).toHaveCount(0)
  await page.reload()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
})
