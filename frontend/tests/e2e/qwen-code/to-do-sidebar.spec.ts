import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { exerciseRelatedTodo, expectRelatedTodoSurvivesReload } from '../helpers/relatedTodoProof'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest('persists the actual native task snapshot in the sidebar after reload', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  const item = 'Keep the native task snapshot'
  await exerciseRelatedTodo(context, { toolCall: updateTodosToolCall(AgentProvider.QWEN_CODE, 'native-todo-sidebar', [{ step: item, status: 'pending' }]), item })
  await expectRelatedTodoSurvivesReload(context, item)
})
