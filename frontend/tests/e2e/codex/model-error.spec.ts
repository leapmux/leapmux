import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

codexTest('reports a native model error and accepts the next turn', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
  void authenticatedCodexWorkspace
  await exerciseModelError({ page, modelScript, provider: AgentProvider.CODEX }, { queueAfterFailure: 'running' })
})
