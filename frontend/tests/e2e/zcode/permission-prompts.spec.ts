import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeRemovalPermission } from './permissionScenario'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

zcodeTest('a risky command produces a permission banner that can be denied', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseZCodeRemovalPermission(context, { bypass: false })
})

zcodeTest('the permission banner applies the selected bypass pill on allow', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseZCodeRemovalPermission(context, { bypass: true })
})
