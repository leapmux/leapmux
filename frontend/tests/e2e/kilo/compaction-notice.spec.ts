import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { kiloTest } from '../kilo-fixtures'
import { OPENCODE_COMPACTION } from '../opencode/compactionScenario'

// Kilo shares OpenCode's adapter limitation, and the worker's own boundary ends
// the compaction: the turn keeps one divider row, live and after a reload
// (decision 21, A27).
kiloTest('ends the actual native compaction with the worker boundary row', async ({ native }) => {
  await exerciseNativeCompaction(native, OPENCODE_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page, { divider: true })
})
