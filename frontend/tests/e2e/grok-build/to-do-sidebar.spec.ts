import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { grokTest } from '../grok-fixtures'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo, expectRelatedTodoSurvivesReload } from '../helpers/relatedTodoProof'

grokTest('persists the actual native task snapshot in the sidebar after reload', async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  const item = 'Keep the native task snapshot'
  await exerciseRelatedTodo(context, { toolCall: updateTodosToolCall(AgentProvider.GROK_BUILD, 'native-todo-sidebar', [{ step: item, status: 'pending' }]), item })
  await expectRelatedTodoSurvivesReload(context, item)
})
