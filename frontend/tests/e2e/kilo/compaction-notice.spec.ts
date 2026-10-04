import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { kiloTest } from '../kilo-fixtures'

kiloTest('completes actual native compaction without a completed notice row', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  await expectNoCompactionNotice({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }, {
    relatedProof: () => exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Create a new anchored summary from the conversation history' }),
  })
})
