import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'

cursorTest('proves the native fast-mode limit after a real sidebar operation', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  const relatedProof = () => exerciseRelatedTodo(context, { toolCall: updateTodosToolCall(AgentProvider.CURSOR, 'related-native-todo', [{ step: 'Native capability proof', status: 'pending' }]), item: 'Native capability proof', singleRequest: true })
  await expectMissingOptionGroup(context, { groupId: 'fastMode', relatedProof })
})
