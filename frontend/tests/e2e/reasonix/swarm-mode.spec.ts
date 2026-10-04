import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('proves the native swarm-mode limit after a real sidebar operation', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  const relatedProof = () => exerciseRelatedTodo(context, { toolCall: updateTodosToolCall(AgentProvider.REASONIX, 'related-native-todo', [{ step: 'Native capability proof', status: 'pending' }]), item: 'Native capability proof', prepare: () => applyPermissionPreset(page, 'bypass') })
  await expectMissingOptionGroup(context, { groupId: 'swarmMode', relatedProof })
})
