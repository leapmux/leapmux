import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseModelError } from '../helpers/nativeModelError'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('shows the native model failure and accepts a later valid prompt', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  await exerciseModelError(context)
})
