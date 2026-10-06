import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { kimiTest } from '../kimi-fixtures'
import { KIMI_COMPACTION } from './compactionScenario'

kimiTest('shows the actual native completed compaction notice and preserves it after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, KIMI_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page)
})
