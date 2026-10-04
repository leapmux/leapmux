import { expect } from '@playwright/test'
import { compactionNoticeRow } from '../helpers/compaction'
import { lettaTest } from '../letta-fixtures'
import { exerciseOrdinaryCompactText } from './compactionScenarios'
import { nativeContext } from './scenarios'

lettaTest('receives no completed compaction notice from the actual native command', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseOrdinaryCompactText(context)
  await expect(compactionNoticeRow(page)).toHaveCount(0)
  await page.reload()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
})
