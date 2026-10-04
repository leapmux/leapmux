import { expect } from '@playwright/test'
import { compactionNoticeRow } from '../helpers/compaction'
import { junieTest } from '../junie-fixtures'
import { exerciseCompressAcknowledgement } from './compactionScenarios'
import { nativeContext } from './scenarios'

junieTest('receives no completed compaction notice from the actual native command', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseCompressAcknowledgement(context)
  await expect(compactionNoticeRow(page)).toHaveCount(0)
  await page.reload()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
})
