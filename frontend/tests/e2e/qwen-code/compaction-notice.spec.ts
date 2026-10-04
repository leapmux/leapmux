import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseManualCompaction, MANUAL_COMPACTION_SUMMARY } from '../helpers/manualCompaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest('proves native context compaction without a completed compaction notice', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await expectNoCompactionNotice(context, { relatedProof: () => exerciseManualCompaction(page, modelScript, { summary: `<analysis>The conversation needs a checkpoint.</analysis><state_snapshot><current_work>${MANUAL_COMPACTION_SUMMARY}</current_work><next_step>Continue the task.</next_step></state_snapshot>`, reportedInputTokens: 10_000, summaryRequestMarker: 'You are the component that summarizes a conversation' }) })
})
