import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { qoderTest } from '../qoder-fixtures'
import { exerciseCompletedManualCompaction } from './compactionScenarios'

qoderTest('keeps the completed native compaction status after reload', async ({ native }) => {
  await exerciseCompletedManualCompaction(native)
  await expectCompactionNoticeAfterReload(native.page)
})
