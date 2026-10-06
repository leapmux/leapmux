import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { junieTest } from '../junie-fixtures'
import { exerciseCompressAcknowledgement } from './compactionScenarios'

junieTest.describe('native manual compaction', () => {
  junieTest('keeps prior context after the native compress acknowledgement', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await exerciseCompressAcknowledgement({ page, modelScript, provider: AgentProvider.JUNIE })
  })
})
