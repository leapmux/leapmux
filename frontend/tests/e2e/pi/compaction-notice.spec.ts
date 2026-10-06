import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { piTest } from '../pi-fixtures'
import { exercisePiEmptyCompaction, PI_COMPACTION } from './compactionScenario'

piTest('replaces a failed native compaction start with its error', async ({ native }) => {
  await exercisePiEmptyCompaction(native)
})

piTest('draws and keeps the native notice after a manual compaction', async ({ native }) => {
  await exerciseNativeCompaction(native, PI_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page)
})
