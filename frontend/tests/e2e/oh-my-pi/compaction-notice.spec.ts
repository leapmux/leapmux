import { expect } from '@playwright/test'
import { compactionIndicator, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { exerciseOhMyPiCompaction } from './compactionScenario'

ohMyPiTest('preserves the native completed compaction notice after a verified summary and reload', async ({ native }) => {
  await exerciseOhMyPiCompaction(native, {
    summaryGate: 'oh-my-pi-manual-summary',
    onHeld: async () => { await expect(compactionIndicator(native.page)).toBeVisible() },
  })
  await expectCompactionNoticeAfterReload(native.page, { count: 1, divider: true })
  await expectNoLiveCompaction(native.page)
})
