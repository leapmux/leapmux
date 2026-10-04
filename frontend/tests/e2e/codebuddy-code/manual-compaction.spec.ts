import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest } from '../codebuddy-fixtures'
import { exerciseContextCompactionWithoutNotice } from './compactionScenarios'

codebuddyTest.describe('CodeBuddy Code compaction notice', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('replaces old context after a native manual compaction without a notice', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await exerciseContextCompactionWithoutNotice({ page, modelScript, provider: AgentProvider.CODEBUDDY })
  })
})
