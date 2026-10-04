import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { piTest } from '../pi-fixtures'

piTest('reopens the native picker session and restores saved Worker messages', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseSessionResume(context)
})
