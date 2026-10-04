import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { kiloTest } from '../kilo-fixtures'

kiloTest('proves the native fast-mode limit after a real sidebar operation', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  const relatedProof = () => exerciseRelatedTodo(context, { toolCall: updateTodosToolCall(AgentProvider.KILO, 'related-native-todo', [{ step: 'Native capability proof', status: 'pending' }]), item: 'Native capability proof' })
  await expectMissingOptionGroup(context, { groupId: 'fastMode', relatedProof })
})
