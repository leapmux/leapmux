import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { piTodoToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo, RELATED_TODO_CALL_ID, RELATED_TODO_ITEM } from '../helpers/relatedTodoProof'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { piTest } from '../pi-fixtures'

piTest('proves the native bypass-permissions-shortcut limit after a real sidebar operation', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  const relatedProof = () => exerciseRelatedTodo(context, { toolCall: piTodoToolCall(RELATED_TODO_CALL_ID, { action: 'create', subject: RELATED_TODO_ITEM }) })
  await expectMissingPermissionShortcut(context, { preset: 'bypass', relatedProof })
})
