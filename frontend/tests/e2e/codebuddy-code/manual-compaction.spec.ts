import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseContextCompactionWithoutNotice } from './compactionScenarios'

codebuddyTest.describe('CodeBuddy Code compaction notice', () => {
  codebuddyTest('replaces old context after a native manual compaction without a notice', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
    await exerciseContextCompactionWithoutNotice({ page, modelScript, provider: AgentProvider.CODEBUDDY })
  })
})
