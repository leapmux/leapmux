import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { LETTA_E2E_SKIP_REASON, lettaTest } from '../letta-fixtures'
import { exerciseOrdinaryCompactText } from './compactionScenarios'

lettaTest.describe('native manual compaction', () => {
  lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

  lettaTest('passes compact text to the model on its App Server path', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await exerciseOrdinaryCompactText({ page, modelScript, provider: AgentProvider.LETTA })
  })
})
