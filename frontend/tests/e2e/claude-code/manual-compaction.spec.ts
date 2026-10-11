import { claudeTest } from '../claude-fixtures'
import { expectCompactionNotice } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { CLAUDE_COMPACTION } from './compactionScenario'

claudeTest.describe('Claude Code compaction notice', () => {
  claudeTest('a manual compaction draws the context-compacted notice', async ({ native }) => {
    await exerciseNativeCompaction(native, CLAUDE_COMPACTION)
    await expectCompactionNotice(native.page)
  })
})
