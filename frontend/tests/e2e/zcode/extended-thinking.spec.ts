import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('proves the native extended-thinking limit after a real sidebar operation', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  const relatedProof = () => exerciseRelatedTodo(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
  await expectMissingOptionGroup(context, { groupId: 'thinking', relatedProof })
})
