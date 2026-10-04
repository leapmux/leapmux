import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

codexTest.describe('codex agent lifecycle', () => {
  codexTest('clear context via /clear command', async ({ authenticatedCodexWorkspace, page, modelScript, leapmuxServer }) => {
    await exerciseSessionReset({ page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId })
  })
})
