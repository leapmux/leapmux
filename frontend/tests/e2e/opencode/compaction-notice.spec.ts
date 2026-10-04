import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('completes actual native compaction without a completed notice row', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  await expectNoCompactionNotice({ page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }, {
    relatedProof: () => exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Create a new anchored summary from the conversation history' }),
  })
})
