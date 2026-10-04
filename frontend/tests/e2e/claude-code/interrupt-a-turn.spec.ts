import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

for (const kind of ['model', 'tool'] as const) {
  claudeTest(`interrupts a held native ${kind} turn and preserves the usable session`, async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
    await exerciseInterruptTurn({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }, { kind })
  })
}
