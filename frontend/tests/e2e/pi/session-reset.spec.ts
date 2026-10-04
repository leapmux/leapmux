import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { PI_E2E_SKIP_REASON, piTest } from '../pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest('clear context via /clear command resets Pi session', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseSessionReset(context)
})

piTest('clears native context without discarding saved Worker messages', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseSessionReset(context)
})
