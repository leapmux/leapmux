import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { qwenTest } from '../qwen-fixtures'
import { QWEN_COMPACTION } from './compactionScenario'

qwenTest('retains the completed native compaction status after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, QWEN_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page)
})
