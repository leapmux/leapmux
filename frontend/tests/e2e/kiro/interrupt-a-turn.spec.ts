import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest.describe('Kiro interrupt, steering and process lifetime', () => {
  kiroTest('interrupts a running turn', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
    await exerciseInterruptTurn(context, { kind: 'model', prompt: 'Write a long report.', divider: /^Turn interrupted$/ })
  })
})
