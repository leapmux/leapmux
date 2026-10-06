import { codewhaleTest } from '../codewhale-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { CODEWHALE_COMPACTION } from './compactionScenario'

codewhaleTest('shows a completed native compaction notice and retains it after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, CODEWHALE_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page, { detail: 'manual' })
})
