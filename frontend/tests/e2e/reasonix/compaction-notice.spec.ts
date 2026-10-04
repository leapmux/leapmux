import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from '../reasonix-fixtures'
import { proveNoNativeManualCompaction } from './compactionScenario'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest('retains native context without a compaction notice after the slash command', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await expectNoCompactionNotice(context, { relatedProof: () => proveNoNativeManualCompaction(page, modelScript) })
})
