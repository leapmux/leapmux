import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest('interrupts an actual held native turn and keeps its session usable during a model call', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseInterruptTurn(context, { kind: 'model' })
})

mimoTest('interrupts an actual held native turn and keeps its session usable during a tool call', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
