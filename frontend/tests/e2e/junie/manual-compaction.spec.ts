import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { JUNIE_E2E_SKIP_REASON, junieTest } from '../junie-fixtures'
import { exerciseCompressAcknowledgement } from './compactionScenarios'

junieTest.describe('native manual compaction', () => {
  junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

  junieTest('keeps prior context after the native compress acknowledgement', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await exerciseCompressAcknowledgement({ page, modelScript, provider: AgentProvider.JUNIE })
  })
})
