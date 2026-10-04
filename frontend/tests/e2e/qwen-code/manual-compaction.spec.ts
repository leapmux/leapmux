import { exerciseManualCompaction, MANUAL_COMPACTION_SUMMARY } from '../helpers/manualCompaction'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('compacts a scripted conversation on request', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    const summary = `<analysis>The conversation needs a checkpoint.</analysis><state_snapshot><current_work>${MANUAL_COMPACTION_SUMMARY}</current_work><next_step>Continue the user's work.</next_step></state_snapshot>`
    await exerciseManualCompaction(page, modelScript, {
      summary,
      reportedInputTokens: 10_000,
      summaryRequestMarker: 'You are the component that summarizes a conversation',
    })
  })
})
