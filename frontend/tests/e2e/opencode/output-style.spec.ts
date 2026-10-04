import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('proves the native output-style limit after a real sidebar operation', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  const relatedProof = () => exerciseRelatedTodo(context, { toolCall: updateTodosToolCall(AgentProvider.OPENCODE, 'related-native-todo', [{ step: 'Native capability proof', status: 'pending' }]), item: 'Native capability proof' })
  await expectMissingOptionGroup(context, { groupId: 'outputStyle', relatedProof })
})
