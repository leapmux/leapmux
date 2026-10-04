import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'

codexTest('reopens the native session and restores its saved UI transcript', async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
  await exerciseSessionResume({ page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId })
})
