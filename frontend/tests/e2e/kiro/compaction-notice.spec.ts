import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest('proves native context compaction without a completed compaction notice', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await expectNoCompactionNotice(context, { relatedProof: () => exerciseManualCompaction(page, modelScript, { completionText: 'Context compacted' }) })
})
