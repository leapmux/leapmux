import { codexTest } from '../codex-fixtures'
import { expectCompactionNotice } from '../helpers/compaction'
import { exerciseCodexCompaction } from './compactionScenario'

codexTest.describe('Codex compaction notice', () => {
  codexTest('a manual compaction draws the notice and removes old context', async ({ native }) => {
    await exerciseCodexCompaction(native)
    await expectCompactionNotice(native.page)
  })
})
