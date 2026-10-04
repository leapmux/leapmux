import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest('proves native context compaction without a completed compaction notice', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await expectNoCompactionNotice(context, { relatedProof: () => exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Write a continuation summary that will allow you' }) })
})
