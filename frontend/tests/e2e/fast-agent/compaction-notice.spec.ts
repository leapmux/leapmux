import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { compactionNoticeRow } from '../helpers/compaction'
import { exerciseCompactRefusal } from './compactionScenarios'
import { nativeContext } from './scenarios'

fastAgentTest('receives no completed compaction notice from the actual native command', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseCompactRefusal(context)
  await expect(compactionNoticeRow(page)).toHaveCount(0)
  await page.reload()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
})
