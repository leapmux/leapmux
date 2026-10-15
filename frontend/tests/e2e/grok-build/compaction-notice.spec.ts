import { grokTest } from '../grok-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { GROK_COMPACTION } from './compactionScenario'

grokTest('retains the completed native compaction status after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, GROK_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page)
})
