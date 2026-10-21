import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { compactionIndicator, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { exerciseCopilotCompaction } from './compactionScenario'

copilotTest('draws and keeps a native manual compaction notice', async ({ native }) => {
  await exerciseCopilotCompaction(native, {
    summaryGate: 'github-copilot-manual-summary',
    onHeld: async () => { await expect(compactionIndicator(native.page)).toBeVisible() },
  })
  await expectCompactionNoticeAfterReload(native.page, { count: 1, divider: true })
  await expectNoLiveCompaction(native.page)
})
