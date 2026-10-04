import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { piTodoToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { piTest } from '../pi-fixtures'

piTest('proves the native mode limit after a real sidebar operation', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  const relatedProof = () => exerciseRelatedTodo(context, { toolCall: piTodoToolCall('related-native-todo', { action: 'create', subject: 'Native capability proof' }), item: 'Native capability proof' })
  await expectMissingOptionGroup(context, { groupId: 'permissionMode', relatedProof })
})
