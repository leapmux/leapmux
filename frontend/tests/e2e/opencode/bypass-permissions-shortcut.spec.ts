import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('proves the native bypass-permissions-shortcut limit after a real sidebar operation', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  const relatedProof = () => exerciseRelatedTodo(context)
  await expectMissingPermissionShortcut(context, { preset: 'bypass', relatedProof })
})
