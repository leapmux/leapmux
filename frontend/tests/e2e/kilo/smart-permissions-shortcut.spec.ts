import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { kiloTest } from '../kilo-fixtures'

kiloTest('proves the native smart-permissions-shortcut limit after a real sidebar operation', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  const relatedProof = () => exerciseRelatedTodo(context)
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof })
})
