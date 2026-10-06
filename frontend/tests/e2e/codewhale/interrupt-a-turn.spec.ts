import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

codewhaleTest('interrupts an actual held native turn and keeps its session usable during a model call', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await exerciseInterruptTurn(context, { kind: 'model' })
})

codewhaleTest('interrupts an actual held native turn and keeps its session usable during a tool call', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
